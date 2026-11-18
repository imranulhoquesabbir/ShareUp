//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(s)
}

// hotspotConnect saves the phone-hotspot as a Wi-Fi profile and asks Windows to connect.
// A failed "connect" is not an error (the hotspot may simply not be on yet); the caller
// keeps retrying until the PC is on a network.
func hotspotConnect(ssid, pass string) error {
	auth, enc, key := "open", "none", ""
	if pass != "" {
		auth, enc = "WPA2PSK", "AES"
		key = "<sharedKey><keyType>passphrase</keyType><protected>false</protected><keyMaterial>" + xmlEsc(pass) + "</keyMaterial></sharedKey>"
	}
	x := `<?xml version="1.0"?><WLANProfile xmlns="http://www.microsoft.com/networking/WLAN/profile/v1"><name>` + xmlEsc(ssid) +
		`</name><SSIDConfig><SSID><name>` + xmlEsc(ssid) + `</name></SSID></SSIDConfig><connectionType>ESS</connectionType><connectionMode>auto</connectionMode><MSM><security><authEncryption><authentication>` +
		auth + `</authentication><encryption>` + enc + `</encryption><useOneX>false</useOneX></authEncryption>` + key + `</security></MSM></WLANProfile>`
	f, err := os.CreateTemp("", "su-wlan-*.xml")
	if err != nil {
		return err
	}
	name := f.Name()
	f.WriteString(x)
	f.Close()
	defer os.Remove(name)
	add := exec.Command("netsh", "wlan", "add", "profile", "filename="+name, "user=current")
	hideWindow(add)
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("Wi-Fi: %s", strings.TrimSpace(string(out)))
	}
	con := exec.Command("netsh", "wlan", "connect", "name="+ssid)
	hideWindow(con)
	con.Run()
	return nil
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
