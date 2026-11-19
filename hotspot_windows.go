//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
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
