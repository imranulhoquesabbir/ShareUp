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
	hotState string // "" | trying | error
	hotErr   string
	hotLast  time.Time
	hotSel   string          // hotspot the person picked
	hotAll   bool            // list every Wi-Fi, not only likely phone hotspots
	hotNets  []string        // hotspots shown to pick from
	hotBase  map[string]bool // Wi-Fi names already in range when the network was lost
	hotJoin  time.Time       // when we last tried to join (zero = not joining)
	hotBad   bool            // the last join did not work (wrong password)
	hotOKAt  atomic.Int64    // unix nano: hotspot joined

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
		time.Sleep(4 * time.Second)
		up := internetUp()
		prev := onlineNow.Swap(up)
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
		maybeHotspot(c, up)
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
	if hotSel != "" {
		hotOKAt.Store(time.Now().UnixNano())
	}
	hotSel, hotAll, hotNets, hotBase, hotBad = "", false, nil, nil, false
	hotJoin = time.Time{}
	hotState, hotErr = "", ""
	hotMu.Unlock()
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
	if c.HotSSID != "" { // optional manual override (Advanced settings)
		hotMu.Lock()
		due := time.Since(hotLast) >= 20*time.Second
		if due {
			hotLast = time.Now()
		}
		hotMu.Unlock()
		if due {
			hotspotConnect(c.HotSSID, c.HotPass)
		}
		return
	}
	vis, saved := wifiScan()
	hotMu.Lock()
	if hotBase == nil {
		hotBase = map[string]bool{}
		for _, v := range vis {
			hotBase[v] = true
		}
	}
	if !hotJoin.IsZero() && time.Since(hotJoin) > 20*time.Second {
		hotBad, hotJoin = true, time.Time{} // joined but still no network: password was wrong
	}
	var phones, others []string
	for _, v := range vis {
		if looksLikePhone(v) {
			phones = append(phones, v)
		} else if hotAll || !hotBase[v] { // a new name appeared: most likely the hotspot just turned on
			others = append(others, v)
		}
	}
	hotNets = append(phones, others...)
	busy := hotSel != ""
	nets := hotNets
	hotMu.Unlock()
	if busy {
		return
	}
	for _, v := range nets { // a hotspot this PC has joined before: no questions
		if saved[v] {
			hotMu.Lock()
			hotSel, hotJoin, hotBad = v, time.Now(), false
			hotMu.Unlock()
			wifiConnect(v)
			return
		}
	}
}

type wifiReq struct {
	Op   string `json:"op"`
	SSID string `json:"ssid"`
	Pass string `json:"pass"`
}

// adminWifi: the answers the person gives to the hotspot popups.
func adminWifi(w http.ResponseWriter, r *http.Request) {
	var q wifiReq
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&q)
	switch q.Op {
	case "all":
		hotMu.Lock()
		hotAll = true
		hotMu.Unlock()
	case "back":
		hotMu.Lock()
		hotSel, hotBad, hotJoin = "", false, time.Time{}
		hotMu.Unlock()
	case "pick":
		q.SSID = strings.TrimSpace(q.SSID)
		if q.SSID == "" || len(q.SSID) > 64 {
			http.Error(w, "bad name", 400)
			return
		}
		_, saved := wifiScan()
		hotMu.Lock()
		hotSel, hotBad, hotJoin = q.SSID, false, time.Time{}
		if saved[q.SSID] {
			hotJoin = time.Now()
		}
		hotMu.Unlock()
		if saved[q.SSID] {
			wifiConnect(q.SSID)
		}
	case "pass":
		hotMu.Lock()
		sel := hotSel
		hotMu.Unlock()
		if sel == "" {
			http.Error(w, "no hotspot", 400)
			return
		}
		if len(q.Pass) > 63 {
			http.Error(w, "password too long", 400)
			return
		}
		if err := hotspotConnect(sel, q.Pass); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		hotMu.Lock()
		hotJoin, hotBad = time.Now(), false
		hotMu.Unlock()
	default:
		http.Error(w, "bad request", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("{}"))
}

// ---------- guide shown on the PC ----------

type guideInfo struct {
	Kind   string `json:"kind"` // "" | offline | back | done | joined
	Step   int    `json:"step"` // offline: 1 turn hotspot on, 2 pick it, 3 password, 4 connecting
	Err    string `json:"err,omitempty"`
	HotSet bool   `json:"hotSet"`
	Nets   []string `json:"nets,omitempty"`
	Sel    string   `json:"sel,omitempty"`
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
	if hasNetwork() {
		if t := hotOKAt.Load(); t != 0 && now-t < int64(8*time.Second) {
			return guideInfo{Kind: "joined"}
		}
		return guideInfo{} // same Wi-Fi / hotspot: the normal QR just works
	}
	g := guideInfo{Kind: "offline", Step: 1, HotSet: c.HotSSID != ""}
	hotMu.Lock()
	switch {
	case hotSel != "" && !hotJoin.IsZero():
		g.Step, g.Sel = 4, hotSel
	case hotSel != "":
		g.Step, g.Sel = 3, hotSel
		if hotBad {
			g.Err = "wrongpass"
		}
	case len(hotNets) > 0:
		g.Step, g.Nets = 2, append([]string(nil), hotNets...)
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
		"hotSsid":    c.HotSSID,
		"hotSet":     c.HotSSID != "",
		"mdns":       fmt.Sprintf("shareup.local:%d", curPort.Load()),
		"tunnel":     remoteSnapshot().Status,
		"guide":      guide(c),
		"busy":       transferBusy(),
	}
}

func smartStart() {
	onlineNow.Store(internetUp())
	go netWatch()
	go smartLoop()
	go mdnsLoop()
}
