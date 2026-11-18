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
