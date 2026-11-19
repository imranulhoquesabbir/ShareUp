//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(s)
}

// profileXML builds a Wi-Fi profile. kind: "wpa2", "wpa3" or "open".
func profileXML(ssid, pass, kind string) string {
	auth, enc, key := "open", "none", ""
	switch kind {
	case "wpa2":
		auth, enc = "WPA2PSK", "AES"
	case "wpa3":
		auth, enc = "WPA3SAE", "AES"
	}
	if kind != "open" {
		key = "<sharedKey><keyType>passphrase</keyType><protected>false</protected><keyMaterial>" + xmlEsc(pass) + "</keyMaterial></sharedKey>"
	}
	return `<?xml version="1.0"?><WLANProfile xmlns="http://www.microsoft.com/networking/WLAN/profile/v1"><name>` + xmlEsc(ssid) +
		`</name><SSIDConfig><SSID><name>` + xmlEsc(ssid) + `</name></SSID></SSIDConfig><connectionType>ESS</connectionType><connectionMode>auto</connectionMode><MSM><security><authEncryption><authentication>` +
		auth + `</authentication><encryption>` + enc + `</encryption><useOneX>false</useOneX></authEncryption>` + key + `</security></MSM></WLANProfile>`
}

// runNetsh runs netsh with the given raw command line. The file path is quoted by hand
// (key="value"): Go would otherwise quote the whole "filename=C:\Users\Some Name\..."
// argument, which netsh rejects as an invalid profile when the path has a space in it.
func runNetsh(raw string) (string, error) {
	c := exec.Command("netsh")
	hideWindow(c)
	c.SysProcAttr.CmdLine = "netsh " + raw
	b, err := c.CombinedOutput()
	return strings.TrimSpace(string(b)), err
}

// hotspotConnect saves the phone-hotspot as a Wi-Fi profile and asks Windows to connect.
// A failed "connect" is not an error (the hotspot may simply not be on yet); the caller
// keeps retrying until the PC is on a network.
func hotspotConnect(ssid, pass string) error {
	if pass != "" && (len(pass) < 8 || len(pass) > 63) {
		return fmt.Errorf("Wi-Fi password must be 8 to 63 characters")
	}
	kinds := []string{"wpa2", "wpa3"}
	if pass == "" {
		kinds = []string{"open"}
	}
	// remove an older / broken profile with this name first
	runNetsh(`wlan delete profile name="` + strings.ReplaceAll(ssid, `"`, "") + `"`)
	last := ""
	for _, kind := range kinds {
		f, err := os.CreateTemp("", "suw*.xml")
		if err != nil {
			return err
		}
		name := f.Name()
		f.WriteString(profileXML(ssid, pass, kind))
		f.Close()
		out, err := runNetsh(`wlan add profile filename="` + name + `" user=current`)
		os.Remove(name)
		if err == nil {
			con := exec.Command("netsh", "wlan", "connect", "name="+ssid)
			hideWindow(con)
			con.Run()
			return nil
		}
		last = out
	}
	return fmt.Errorf("Wi-Fi: %s", last)
}

// wifiRescan asks the Wi-Fi adapter to look for networks again. "netsh wlan show networks"
// only shows what Windows scanned a while ago, so without this a hotspot that was just
// turned on can take a minute to appear.
func wifiRescan() {
	dll := syscall.NewLazyDLL("wlanapi.dll")
	if dll.Load() != nil {
		return
	}
	open, enum, scan := dll.NewProc("WlanOpenHandle"), dll.NewProc("WlanEnumInterfaces"), dll.NewProc("WlanScan")
	free, closeH := dll.NewProc("WlanFreeMemory"), dll.NewProc("WlanCloseHandle")
	var ver uint32
	var h uintptr
	if r, _, _ := open.Call(2, 0, uintptr(unsafe.Pointer(&ver)), uintptr(unsafe.Pointer(&h))); r != 0 {
		return
	}
	defer closeH.Call(h, 0)
	var list uintptr
	if r, _, _ := enum.Call(h, 0, uintptr(unsafe.Pointer(&list))); r != 0 || list == 0 {
		return
	}
	defer free.Call(list)
	// WLAN_INTERFACE_INFO_LIST: DWORD count, DWORD index, then WLAN_INTERFACE_INFO items
	// (GUID 16 bytes + WCHAR[256] description + DWORD state = 532 bytes each).
	n := *(*uint32)(unsafe.Pointer(list))
	for i := uint32(0); i < n && i < 8; i++ {
		guid := list + 8 + uintptr(i)*532
		scan.Call(h, guid, 0, 0, 0)
	}
}

func netshOut(args ...string) string {
	c := exec.Command("netsh", args...)
	hideWindow(c)
	b, _ := c.CombinedOutput()
	return string(b)
}

// wifiScan lists the Wi-Fi names in range, and which of them this PC has already saved
// (so no password is needed for those).
func wifiScan() ([]string, map[string]bool) {
	wifiRescan()
	saved := map[string]bool{}
	for _, ln := range strings.Split(netshOut("wlan", "show", "profiles"), "\n") {
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(ln[:i]))
		val := strings.TrimSpace(ln[i+1:])
		if val != "" && strings.Contains(key, "profile") {
			saved[val] = true
		}
	}
	var vis []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(netshOut("wlan", "show", "networks"), "\n") {
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(ln[:i]))
		val := strings.TrimSpace(ln[i+1:])
		if val == "" || !strings.HasPrefix(key, "ssid") || seen[val] {
			continue
		}
		seen[val] = true
		vis = append(vis, val)
	}
	return vis, saved
}

// wifiConnect asks Windows to join a Wi-Fi it already has a saved profile for.
func wifiConnect(name string) error {
	con := exec.Command("netsh", "wlan", "connect", "name="+name)
	hideWindow(con)
	if out, err := con.CombinedOutput(); err != nil {
		return fmt.Errorf("Wi-Fi: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
