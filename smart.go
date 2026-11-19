package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
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

// Transfers in progress. The app never changes the PC's network (hotspot) or restarts the
// internet link while a file is being sent, so nothing is cut in half.
var (
	xferActive atomic.Int32
	xferLast   atomic.Int64 // unix nano of the last upload/download activity
)

func transferBusy() bool {
	if xferActive.Load() > 0 {
		return true
	}
	t := xferLast.Load()
	return t != 0 && time.Now().UnixNano()-t < int64(20*time.Second)
}

func trackXfer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		xferActive.Add(1)
		xferLast.Store(time.Now().UnixNano())
		defer func() {
			xferActive.Add(-1)
			xferLast.Store(time.Now().UnixNano())
		}()
		next(w, r)
	}
}

var (
	onlineNow  atomic.Bool
	wasOffline atomic.Bool
	backAt     atomic.Int64 // unix nano: internet came back
	doneAt     atomic.Int64 // unix nano: a phone connected while offline

	hotMu    sync.Mutex
	hotLast  time.Time
	hotSel   string          // hotspot the person picked
	hotNets  []string        // hotspots shown to pick from
	hotBase  map[string]bool // Wi-Fi names already in range when the network was lost
	hotJoin  time.Time       // when we last tried to join (zero = not joining)
	hotOKAt  atomic.Int64    // unix nano: hotspot joined
	hotEpi   bool            // this PC was without any network (a "no network" episode is running)
	hotPanel bool            // the Wi-Fi list was already opened by itself in this episode
	hotSeen  map[string]int  // how many scans in a row each Wi-Fi name has been in range
	hotJoinNet string        // name of the Wi-Fi the PC joined (shown with the green tick)

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

// defaultWorker is baked into the build (-X main.defaultWorker=https://...) so users of the
// released app need no setup. A URL saved in Settings overrides it.
var defaultWorker = "https://shareup.imranul-hoque-sabbir.workers.dev"

func workerBase(c Config) string {
	u := strings.TrimRight(strings.TrimSpace(c.WorkerURL), "/")
	if u == "" {
		u = strings.TrimRight(strings.TrimSpace(defaultWorker), "/")
	}
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

// virtualAdapter spots adapters that exist even when the PC is on no real network
// (WSL / Hyper-V / VM / VPN / Docker / Bluetooth ...). They must not count as "online".
func virtualAdapter(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"tun", "utun", "tap", "veth", "br-", "virbr", "vboxnet", "vmnet", "docker", "awdl", "llw", "ap1", "gif", "stf", "anpi"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, p := range []string{"vethernet", "wsl", "virtualbox", "vmware", "hyper-v", "bluetooth", "loopback",
		"tailscale", "zerotier", "vpn", "npcap", "docker", "host-only"} {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// hasNetwork is true only when a real (non-virtual) adapter is up and has an IPv4 address.
// A disconnected Wi-Fi is reported as down by the OS, so this turns false right away.
func hasNetwork() bool {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || virtualAdapter(i.Name) {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if v4 := n.IP.To4(); v4 != nil && !v4.IsLinkLocalUnicast() && !v4.IsLoopback() {
					return true
				}
			}
		}
	}
	return false
}

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
		select {
		case <-time.After(4 * time.Second):
		case <-kickNW: // the network just changed: check right now
		}
		up := internetUp()
		prev := onlineNow.Swap(up)
		if prev != up {
			notifyState()
			kickHotspot()
		}
		if !up {
			wasOffline.Store(true)
		} else if !prev && wasOffline.Load() {
			backAt.Store(time.Now().UnixNano())
			kickUpdate() // look for a new version right away
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
		if !up {
			continue
		}
		if c.AutoNet { // keep the internet link ready by itself
			st := remoteSnapshot().Status
			if (st == "off" || st == "error") && time.Since(tunTry) > 30*time.Second && !transferBusy() {
				tunTry = time.Now()
				remoteStart()
			}
		}
		base := workerBase(c)
		if base == "" {
			continue
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

var phoneHints = []string{"iphone", "android", "galaxy", "samsung", "redmi", "xiaomi", "poco", "pixel", "oppo", "vivo",
	"realme", "infinix", "tecno", "itel", "oneplus", "huawei", "honor", "motorola", "nokia", "symphony", "walton", "hotspot"}

// wifiAP is one Wi-Fi network in range.
type wifiAP struct {
	Name  string
	Sig   int  // signal strength in %, 0 when unknown
	Local bool // the radio address is "locally administered": what a phone hotspot uses, a router almost never does
}

func looksLikePhone(name string) bool {
	n := strings.ToLower(name)
	for _, h := range phoneHints {
		if strings.Contains(n, h) {
			return true
		}
	}
	return false
}

func hotReset() {
	hotMu.Lock()
	was := hotSel != "" || hotEpi // the PC got a network again after a "no network" episode
	hotMu.Unlock()
	name := ""
	if was {
		name = wifiCurrent() // asked outside the lock: it runs a Windows command
	}
	hotMu.Lock()
	if was {
		hotJoinNet = name
		hotOKAt.Store(time.Now().UnixNano()) // show the green "connected" note
	}
	hotEpi, hotPanel = false, false
	hotSel, hotNets, hotBase, hotSeen = "", nil, nil, nil
	hotJoin = time.Time{}
	hotMu.Unlock()
	if was {
		notifyState()
	}
}

// maybeHotspot runs every few seconds. With no network at all it looks for a phone hotspot,
// joins one the PC already knows by itself, and otherwise lets the guide ask the person
// one small thing at a time (turn it on, pick it, type the password).
func maybeHotspot(c Config, up bool) {
	if up || hasNetwork() {
		hotReset()
		return
	}
	if sessionCount() > 0 || transferBusy() {
		return
	}
	hotMu.Lock()
	hotEpi = true
	hotMu.Unlock()
	aps, saved := wifiScan()
	cur := wifiCurrent() // the Wi-Fi this PC is on right now is never offered as a hotspot
	hotMu.Lock()
	if hotBase == nil {
		hotBase = map[string]bool{}
		for _, a := range aps {
			hotBase[a.Name] = true
		}
	}
	if hotSeen == nil {
		hotSeen = map[string]int{}
	}
	inRange := map[string]bool{}
	for _, a := range aps {
		inRange[a.Name] = true
		hotSeen[a.Name]++
	}
	for n := range hotSeen { // a name that vanished for one scan starts counting again
		if !inRange[n] {
			delete(hotSeen, n)
		}
	}
	if !hotJoin.IsZero() && time.Since(hotJoin) > 20*time.Second {
		hotSel, hotJoin, hotLast = "", time.Time{}, time.Now() // auto-join did not work: back to the guide
	}
	// Wi-Fi scans are noisy: neighbours' routers blink in and out of range. So a name is only
	// offered when it has stayed for several scans in a row, and a name that does not look
	// like a phone must also use a phone-style (locally administered) radio address, have
	// been absent when the network was lost, and be reasonably strong.
	var phones, others []string
	for _, a := range aps {
		if a.Name == cur {
			continue
		}
		seen := hotSeen[a.Name]
		switch {
		case looksLikePhone(a.Name):
			if seen >= 2 {
				phones = append(phones, a.Name)
			}
		case a.Local && !hotBase[a.Name] && seen >= 3 && (a.Sig == 0 || a.Sig >= 50):
			others = append(others, a.Name)
		}
	}
	sort.Strings(phones) // stable order, so the list does not jump around on screen
	sort.Strings(others)
	nets := append(append([]string(nil), phones...), others...)
	changed := strings.Join(nets, "\x00") != strings.Join(hotNets, "\x00")
	hotNets = nets
	busy := hotSel != "" || time.Since(hotLast) < 30*time.Second
	openIt := len(phones) > 0 && !hotPanel // a phone hotspot just showed up: open the Wi-Fi list for the person
	if openIt {
		hotPanel = true
	}
	hotMu.Unlock()
	if changed {
		notifyState()
	}
	if openIt {
		go openWifiPanel()
	}
	if busy {
		return
	}
	for _, v := range nets { // a hotspot this PC has joined before: no questions
		if saved[v] {
			hotMu.Lock()
			hotSel, hotJoin = v, time.Now()
			hotMu.Unlock()
			wifiConnect(v)
			return
		}
	}
}

// adminWifi: the "Open Wi-Fi list" button of the hotspot guide.
func adminWifi(w http.ResponseWriter, r *http.Request) {
	go openWifiPanel()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("{}"))
}

// ---------- guide shown on the PC ----------

type guideInfo struct {
	Kind string   `json:"kind"` // "" | offline | back | done | joined
	Step int      `json:"step"` // offline: 1 guide, 4 joining a hotspot this PC already knows
	Nets []string `json:"nets,omitempty"`
	Sel  string   `json:"sel,omitempty"`
	Net  string   `json:"net,omitempty"` // joined: the Wi-Fi this PC is on now
}

func guide(c Config) guideInfo {
	now := time.Now().UnixNano()
	if t := hotOKAt.Load(); t != 0 && now-t < int64(10*time.Second) {
		hotMu.Lock()
		n := hotJoinNet
		hotMu.Unlock()
		return guideInfo{Kind: "joined", Net: n}
	}
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
	if hasNetwork() {
		return guideInfo{} // same Wi-Fi / hotspot: the normal QR just works
	}
	g := guideInfo{Kind: "offline", Step: 1}
	hotMu.Lock()
	if hotSel != "" && !hotJoin.IsZero() {
		g.Step, g.Sel = 4, hotSel // joining a hotspot this PC already knows
	} else if len(hotNets) > 0 {
		g.Nets = append([]string(nil), hotNets...) // a phone hotspot is in range
	}
	hotMu.Unlock()
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
		"mdns":       fmt.Sprintf("shareup.local:%d", curPort.Load()),
		"tunnel":     remoteSnapshot().Status,
		"guide":      guide(c),
		"busy":       transferBusy(),
	}
}

var kickHot = make(chan struct{}, 1)

// kickHotspot makes hotLoop look at the network now instead of waiting for its timer.
func kickHotspot() {
	select {
	case kickHot <- struct{}{}:
	default:
	}
}

// hotLoop watches for "no network" and a phone hotspot. It runs on its own so that a slow
// Wi-Fi scan never delays the internet check or the worker registration. While the PC has
// no network it looks every 2 s, otherwise every 5 s (or at once when something changes).
func hotLoop() {
	for {
		maybeHotspot(get(), onlineNow.Load())
		wait := 5 * time.Second
		hotMu.Lock()
		if hotEpi {
			wait = 2 * time.Second
		}
		hotMu.Unlock()
		select {
		case <-time.After(wait):
		case <-kickHot:
		}
	}
}

func smartStart() {
	onlineNow.Store(internetUp())
	go netWatch()
	go netFast()
	go hotLoop()
	go smartLoop()
	go mdnsLoop()
}
