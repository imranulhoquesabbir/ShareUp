//go:build !windows

package main

import "errors"

func hotspotConnect(ssid, pass string) error {
	return errors.New("Hotspot auto-connect works on Windows only")
}

func wifiScan() ([]string, map[string]bool) { return nil, map[string]bool{} }

func wifiConnect(name string) error {
	return errors.New("Hotspot auto-connect works on Windows only")
}
