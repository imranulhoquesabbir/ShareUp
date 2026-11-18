//go:build !windows

package main

import "errors"

func hotspotConnect(ssid, pass string) error {
	return errors.New("Hotspot auto-connect works on Windows only")
}
