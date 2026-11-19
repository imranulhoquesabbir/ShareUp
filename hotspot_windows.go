//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// openWifiPanel opens the Windows Wi-Fi list (the same flyout as the taskbar Wi-Fi icon),
// so the person can pick their phone's Hotspot there. Older Windows falls back to Settings.
func openWifiPanel() {
	c := exec.Command("cmd", "/c", "start", "", "ms-availablenetworks:")
	hideWindow(c)
	if c.Run() != nil {
		f := exec.Command("cmd", "/c", "start", "", "ms-settings:network-wifi")
		hideWindow(f)
		f.Run()
	}
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

// netshOut runs netsh with a time limit: a stuck Wi-Fi driver must never freeze the app.
func netshOut(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "netsh", args...)
	hideWindow(c)
	b, _ := c.CombinedOutput()
	return string(b)
}

// isMAC reports whether v looks like aa:bb:cc:dd:ee:ff.
func isMAC(v string) bool {
	if len(v) != 17 {
		return false
	}
	for i := 0; i < 17; i++ {
		c := v[i]
		if i%3 == 2 {
			if c != ':' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// localMAC: the "locally administered" bit of the first byte. Phone hotspots use such
// addresses; routers almost always use a factory (universal) one.
func localMAC(v string) bool {
	n, err := strconv.ParseUint(v[:2], 16, 8)
	return err == nil && n&0x02 != 0
}

// wifiScan lists the Wi-Fi networks in range (with signal and whether the radio address
// looks like a phone's), and which of them this PC has already saved (no password needed).
func wifiScan() ([]wifiAP, map[string]bool) {
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
	var vis []wifiAP
	idx := map[string]int{}
	cur := -1
	for _, ln := range strings.Split(netshOut("wlan", "show", "networks", "mode=bssid"), "\n") {
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(ln[:i]))
		val := strings.TrimSpace(ln[i+1:])
		switch {
		case strings.HasPrefix(key, "bssid"):
			if cur >= 0 && isMAC(val) && localMAC(val) {
				vis[cur].Local = true
			}
		case strings.HasPrefix(key, "ssid"):
			cur = -1
			if val == "" {
				continue
			}
			if k, ok := idx[val]; ok {
				cur = k
				continue
			}
			idx[val] = len(vis)
			vis = append(vis, wifiAP{Name: val})
			cur = len(vis) - 1
		case strings.HasPrefix(key, "signal"):
			if n, err := strconv.Atoi(strings.TrimSuffix(val, "%")); err == nil && cur >= 0 && n > vis[cur].Sig {
				vis[cur].Sig = n
			}
		}
	}
	return vis, saved
}

// wifiCurrent is the name of the Wi-Fi this PC is connected to right now ("" if none).
func wifiCurrent() string {
	for _, ln := range strings.Split(netshOut("wlan", "show", "interfaces"), "\n") {
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(ln[:i]))
		val := strings.TrimSpace(ln[i+1:])
		if strings.HasPrefix(key, "ssid") && val != "" { // "BSSID" starts with "b", so it is not matched
			return val
		}
	}
	return ""
}

// wifiConnect asks Windows to join a Wi-Fi it already has a saved profile for.
func wifiConnect(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	con := exec.CommandContext(ctx, "netsh", "wlan", "connect", "name="+name)
	hideWindow(con)
	if out, err := con.CombinedOutput(); err != nil {
		return fmt.Errorf("Wi-Fi: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
