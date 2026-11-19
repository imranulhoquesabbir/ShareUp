// Copyright (c) 2026 Imranul Hoque Sabbir. All rights reserved.

package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Injected by the GitHub workflow (${{ github.repository }} / tag), so whichever repo
// builds the app is the repo it checks for updates. No name is hardcoded.
//
//	-X main.version=<tag>  -X main.repo=<owner/name>
var (
	version = "dev"
	repo    = "" // filled automatically from the GitHub repo that builds the app
)

// uiUpdateHook lets the tray react when a new build is found (set on Windows).
var uiUpdateHook func(tag string)

type updateState struct {
	Enabled   bool   `json:"enabled"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	URL       string `json:"url"`
	Status    string `json:"status"` // idle | checking | uptodate | installing | error
	Error     string `json:"error"`
	Progress  int    `json:"progress"`
	Phase     string `json:"phase,omitempty"` // while installing: download | unpack | restart
	Checked   int64  `json:"checked"`
}

var (
	umu    sync.Mutex
	us     = updateState{Status: "idle"}
	uAsset string
	uZip   bool // uAsset is shareUp.zip (holds every build); otherwise the single-file build
	uBusy  bool
)

func updateRepo() string {
	if c := get(); strings.Contains(c.UpdateRepo, "/") {
		return c.UpdateRepo
	}
	return repo
}

func updateSnapshot() updateState {
	umu.Lock()
	defer umu.Unlock()
	s := us
	s.Enabled = get().AutoUpdate
	s.Current = version
	return s
}

func assetWanted() string {
	switch runtime.GOOS {
	case "windows":
		return "shareUp.exe"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "shareUp-mac-arm64"
		}
		return "shareUp-mac-intel"
	}
	return "shareUp-linux"
}

// checkUpdate asks GitHub for the latest release. Any tag different from this build counts as new.
func checkUpdate() {
	umu.Lock()
	if uBusy || us.Status == "installing" {
		umu.Unlock()
		return
	}
	uBusy = true
	us.Status = "checking"
	us.Error = ""
	umu.Unlock()
	defer func() { umu.Lock(); uBusy = false; umu.Unlock(); notifyState() }()

	fail := func(msg string) {
		umu.Lock()
		us.Status, us.Error, us.Phase = "error", msg, ""
		umu.Unlock()
		notifyState()
	}
	r := updateRepo()
	if !strings.Contains(r, "/") {
		fail("This build does not know its GitHub repo (build it with the GitHub workflow).")
		return
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+r+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "shareUp-updater")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		fail("Could not reach GitHub.")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		umu.Lock()
		us.Status, us.Latest, us.Available, us.Checked = "uptodate", "", false, time.Now().Unix()
		umu.Unlock()
		return
	}
	if resp.StatusCode != 200 {
		fail(fmt.Sprintf("GitHub answered %d.", resp.StatusCode))
		return
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel) != nil || rel.Tag == "" {
		fail("Unexpected reply from GitHub.")
		return
	}
	asset, isZip := "", false
	for _, a := range rel.Assets {
		if a.Name == assetWanted() {
			asset, isZip = a.URL, false // best: only the build for this system (small, quick download)
			break
		}
		if a.Name == "shareUp.zip" {
			asset, isZip = a.URL, true // fallback: one zip with every build (4x bigger)
		}
	}
	avail := rel.Tag != version && version != "dev"
	umu.Lock()
	us.Latest, us.URL, uAsset, uZip = rel.Tag, rel.URL, asset, isZip
	us.Available = avail
	us.Status = "uptodate"
	us.Checked = time.Now().Unix()
	umu.Unlock()
	if avail && uiUpdateHook != nil {
		uiUpdateHook(rel.Tag)
	}
}

// installUpdate downloads the new build, swaps it in, and restarts. It only runs when the
// person asks for it.
func installUpdate() {
	if transferBusy() {
		umu.Lock()
		us.Status, us.Error = "error", "A file is being sent right now. Try again when it finishes."
		umu.Unlock()
		return
	}
	umu.Lock()
	if us.Status == "installing" || !us.Available || uAsset == "" {
		if uAsset == "" && us.Available {
			us.Status, us.Error = "error", "This release has no file for your system."
		}
		umu.Unlock()
		return
	}
	us.Status, us.Error, us.Progress, us.Phase = "installing", "", 0, "download"
	url, isZip := uAsset, uZip
	umu.Unlock()
	notifyState()

	fail := func(msg string) {
		umu.Lock()
		us.Status, us.Error, us.Phase = "error", msg, ""
		umu.Unlock()
		notifyState()
	}
	exe := exePath()
	tmp := exe + ".new"
	dl := tmp // where the download lands (the zip is unpacked into tmp afterwards)
	if isZip {
		dl = exe + ".zip"
	}
	resp, err := (&http.Client{}).Get(url)
	if err != nil || resp.StatusCode != 200 {
		fail("Download failed.")
		return
	}
	defer resp.Body.Close()
	f, err := os.Create(dl)
	if err != nil {
		fail("Cannot write next to the app: " + err.Error())
		return
	}
	var done int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(dl)
				fail("Download failed.")
				return
			}
			done += int64(n)
			if resp.ContentLength > 0 {
				p := int(done * 100 / resp.ContentLength)
				umu.Lock()
				changed := p != us.Progress
				us.Progress = p
				umu.Unlock()
				if changed {
					notifyState() // the progress moves smoothly in the window
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				f.Close()
				os.Remove(dl)
				fail("Download interrupted.")
				return
			}
			break
		}
	}
	f.Close()
	if done < 1<<20 {
		os.Remove(dl)
		fail("Downloaded file looks wrong.")
		return
	}
	if isZip {
		umu.Lock()
		us.Phase = "unpack" // copying the build out of the zip can take a few seconds: say so
		umu.Unlock()
		notifyState()
		err := extractFromZip(dl, assetWanted(), tmp)
		os.Remove(dl)
		if err != nil {
			os.Remove(tmp)
			fail("The update file is incomplete.")
			return
		}
	}
	os.Chmod(tmp, 0755)
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		fail("Cannot replace the app: " + err.Error())
		return
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe)
		fail("Cannot replace the app: " + err.Error())
		return
	}
	args := []string{"--after-update"}
	quiet := false
	for _, a := range os.Args[1:] {
		if a == "--startup" {
			quiet = true
		}
	}
	if quiet && !windowOpen() { // window was open: reopen it after the restart
		args = append(args, "--startup")
	}
	umu.Lock()
	us.Phase = "restart"
	umu.Unlock()
	notifyState()
	time.Sleep(400 * time.Millisecond) // the window shows "Restarting…" before it goes away
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		os.Rename(exe, tmp)
		os.Rename(old, exe)
		fail("Could not restart: " + err.Error())
		return
	}
	if srv != nil {
		srv.Close() // free the port at once so the new copy can take it
	}
	remoteStop()
	uiQuit() // remove the old tray icon properly, otherwise a dead second icon stays behind
	os.Exit(0)
}

// extractFromZip copies one named file out of the zip (the build for this system).
func extractFromZip(zipPath, name, out string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.Name != name {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, rc)
		f.Close()
		if err != nil || n < 1<<20 {
			os.Remove(out)
			return fmt.Errorf("bad file in zip")
		}
		return nil
	}
	return fmt.Errorf("%s not in zip", name)
}

func cleanupOldBuild() {
	os.Remove(exePath() + ".zip")
	os.Remove(exePath() + ".old")
	os.Remove(exePath() + ".new")
}

var updKick = make(chan struct{}, 1)

// kickUpdate asks for an update check now (window opened, internet came back).
// At most one check a minute so GitHub is not hammered.
func kickUpdate() {
	umu.Lock()
	recent := time.Now().Unix()-us.Checked < 60
	umu.Unlock()
	if recent {
		return
	}
	select {
	case updKick <- struct{}{}:
	default:
	}
}

// updateLoop checks at start, every 5 minutes, and whenever kickUpdate is called.
// It only LOOKS for a new version. Nothing is downloaded, replaced or restarted until the
// person presses "Update now" (window banner or tray menu), so the app never changes by itself.
func updateLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if get().AutoUpdate {
			checkUpdate()
		}
		select {
		case <-t.C:
		case <-updKick:
		}
	}
}

func adminUpdate(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Check   bool `json:"check"`
		Install bool `json:"install"`
	}
	json.NewDecoder(r.Body).Decode(&q)
	if q.Check {
		go checkUpdate()
	}
	if q.Install {
		go installUpdate()
	}
	time.Sleep(80 * time.Millisecond)
	adminState(w, r)
}

var _ = filepath.Join
