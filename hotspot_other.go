//go:build !windows

package main

import "errors"

func openWifiPanel() {}

func wifiScan() ([]wifiAP, map[string]bool) { return nil, map[string]bool{} }

func wifiCurrent() string { return "" }

func wifiConnect(name string) error {
	return errors.New("Hotspot auto-connect works on Windows only")
}
