package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Smart connection:
//   - a fixed QR (Cloudflare Worker link) that never changes; the Worker sends each phone to
//     the PC's current LAN address (same network) or its current tunnel (anywhere else)
//   - the PC tells the Worker its current addresses (register loop) and keeps the internet tunnel up
//   - when the internet is gone: step-by-step guide, Offline mode, phone-hotspot auto connect

var (
	onlineNow  atomic.Bool
	wasOffline atomic.Bool
	backAt     atomic.Int64 // unix nano: internet came back
	doneAt     atomic.Int64 // unix nano: a phone connected while offline

	hotMu    sync.Mutex
	hotState string // "" | trying | error
	hotErr   string
	hotLast  time.Time

	regMu   sync.Mutex
	regOK   bool
	regErr  string
	regLast time.Time
	regSig  string
)

func randID(n int) string {
	b := make([]byte, n/2)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func workerBase(c Config) string {
	u := strings.TrimRight(strings.TrimSpace(c.WorkerURL), "/")
	if !strings.HasPrefix(u, "https://") || strings.ContainsAny(u, " \t\r\n?#") {
		return ""
	}
	return u
}

func fixedURL(c Config) string {
	b := workerBase(c)
	if b == "" || c.PubID == "" {
		return ""
	}
	return b + "/d/" + c.PubID
}

func smartReset() {
	regMu.Lock()
	regSig, regOK, regErr = "", false, ""
	regLast = time.Time{}
	regMu.Unlock()
}

// ---------- network checks ----------

func internetUp() bool {
	for _, a := range []string{"1.1.1.1:443", "8.8.8.8:443"} {
		c, err := net.DialTimeout("tcp", a, 2500*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// routeIP is the local address the OS would use to reach the outside (the real network
// adapter, not WSL/VM adapters). "" when the PC is not on any network.
func routeIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
		return a.IP.String()
	}
	return ""
}

func hasNetwork() bool { return routeIP() != "" }

func lanURL() string {
	ip := pickIP(get(), localIPs())
	if ip == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", ip, curPort.Load())
}

func sessionCount() int {
	dmu.Lock()
	defer dmu.Unlock()
	return len(sessions)
}

func netWatch() {
	last := 0
	for {
		time.Sleep(4 * time.Second)
		up := internetUp()
		prev := onlineNow.Swap(up)
		if !up {
			wasOffline.Store(true)
		} else if !prev && wasOffline.Load() {
			backAt.Store(time.Now().UnixNano())
		}
		n := sessionCount()
		if !up && n > 0 && last == 0 {
			doneAt.Store(time.Now().UnixNano())
		}
		last = n
	}
}

// ---------- Worker registration + automatic tunnel + hotspot ----------

func smartLoop() {
	var tunTry time.Time
	for {
		time.Sleep(5 * time.Second)
		c := get()
		up := onlineNow.Load()
		maybeHotspot(c, up)
		base := workerBase(c)
		if base == "" || !up {
			continue
		}
		if c.AutoNet {
			st := remoteSnapshot().Status
			if (st == "off" || st == "error") && time.Since(tunTry) > 30*time.Second {
				tunTry = time.Now()
				remoteStart()
			}
		}
		tunnel := ""
		if rs := remoteSnapshot(); rs.Status == "on" {
			tunnel = rs.Link
		}
		lan := lanURL()
		if lan == "" && tunnel == "" {
			continue
		}
		sig := lan + "|" + tunnel
		regMu.Lock()
		need := sig != regSig || time.Since(regLast) > 15*time.Minute || (!regOK && time.Since(regLast) > 20*time.Second)
		regMu.Unlock()
		if need {
			registerWorker(c, base, lan, tunnel, sig)
		}
	}
}

func registerWorker(c Config, base, lan, tunnel, sig string) {
	lans := []string{}
	if lan != "" {
		lans = append(lans, lan)
	}
	body, _ := json.Marshal(map[string]any{"key": c.DevKey, "lan": lans, "tunnel": tunnel})
	cl := http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Post(base+"/r/"+c.PubID, "application/json", bytes.NewReader(body))
	regMu.Lock()
	defer regMu.Unlock()
	regLast = time.Now()
	if err != nil {
		regOK, regErr = false, "Cannot reach Worker"
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		regOK, regErr, regSig = true, "", sig
	case 403:
		regOK, regErr = false, "This QR ID belongs to another key. Use Reset fixed QR."
	case 500:
		regOK, regErr = false, "Worker setup incomplete (KV binding SU missing?)"
	default:
		regOK, regErr = false, fmt.Sprintf("Worker answered %d", resp.StatusCode)
	}
}

func maybeHotspot(c Config, up bool) {
	if !c.OfflineOn || up || c.HotSSID == "" || hasNetwork() {
		hotMu.Lock()
		hotState, hotErr = "", ""
		hotMu.Unlock()
		return
	}
	hotMu.Lock()
	if time.Since(hotLast) < 20*time.Second {
		hotMu.Unlock()
		return
	}
	hotLast = time.Now()
	hotState = "trying"
	hotMu.Unlock()
	err := hotspotConnect(c.HotSSID, c.HotPass)
	hotMu.Lock()
	if err != nil {
		msg := err.Error()
		if len(msg) > 120 {
			msg = msg[:120]
		}
		hotState, hotErr = "error", msg
	} else {
		hotState, hotErr = "trying", ""
	}
	hotMu.Unlock()
}

// ---------- guide shown on the PC ----------

type guideInfo struct {
	Kind   string `json:"kind"` // "" | offline | back | done
	Step   int    `json:"step"` // offline: 1 offline mode, 2 network/hotspot, 3 scan
	Err    string `json:"err,omitempty"`
	HotSet bool   `json:"hotSet"`
}

func guide(c Config) guideInfo {
	now := time.Now().UnixNano()
	if onlineNow.Load() {
		if t := backAt.Load(); t != 0 && now-t < int64(8*time.Second) {
			return guideInfo{Kind: "back"}
		}
		return guideInfo{}
	}
	if t := doneAt.Load(); t != 0 && now-t < int64(5*time.Second) {
		return guideInfo{Kind: "done"}
	}
	if sessionCount() > 0 {
		return guideInfo{}
	}
	g := guideInfo{Kind: "offline", HotSet: c.HotSSID != ""}
	switch {
	case !c.OfflineOn:
		g.Step = 1
	case !hasNetwork():
		g.Step = 2
		hotMu.Lock()
		if hotState == "error" {
			g.Err = hotErr
		}
		hotMu.Unlock()
	default:
		g.Step = 3
	}
	return g
}

func smartSnapshot(c Config) map[string]any {
	regMu.Lock()
	ok, er := regOK, regErr
	regMu.Unlock()
	return map[string]any{
		"configured": workerBase(c) != "",
		"fixedUrl":   fixedURL(c),
		"online":     onlineNow.Load(),
		"registered": ok,
		"regErr":     er,
		"offlineOn":  c.OfflineOn,
		"autoNet":    c.AutoNet,
		"workerUrl":  c.WorkerURL,
		"hotSsid":    c.HotSSID,
		"hotSet":     c.HotSSID != "",
		"mdns":       fmt.Sprintf("shareup.local:%d", curPort.Load()),
		"tunnel":     remoteSnapshot().Status,
		"guide":      guide(c),
	}
}

func smartStart() {
	onlineNow.Store(internetUp())
	go netWatch()
	go smartLoop()
	go mdnsLoop()
}
