package main

import "net/http"

// Who may use the files shared from this PC.
//
//   - ShareOn is the master switch: off = nobody.
//   - ShareOnlySel picks the default for devices that have no setting of their own:
//     false = every device may (the old behaviour), true = only devices set to "always".
//   - A device can have its own setting, remembered by its id, so it also holds the next time
//     it connects: "off" = never, "on" = always (while the master switch is on).

// devShareOK says whether one device may use the shared files (master switch not included).
func devShareOK(c Config, id string) bool {
	switch c.DevShare[id] {
	case "on":
		return true
	case "off":
		return false
	}
	return !c.ShareOnlySel
}

// shareAllowedFor says whether the phone that made this request may see and download the shared files.
func shareAllowedFor(r *http.Request, c Config) bool {
	if !c.ShareOn || c.ShareDir == "" {
		return false
	}
	dmu.Lock()
	s := sessionFrom(r)
	dmu.Unlock()
	if s == nil {
		return false
	}
	return devShareOK(c, s.DevID)
}

// setDevShare saves one device's setting: "on", "off", or "" for the default.
// The map is replaced, never changed in place, because get() hands copies of the config around.
func setDevShare(id, mode string) {
	mu.Lock()
	m := make(map[string]string, len(cfg.DevShare)+1)
	for k, v := range cfg.DevShare {
		m[k] = v
	}
	if mode == "on" || mode == "off" {
		m[id] = mode
	} else {
		delete(m, id)
	}
	cfg.DevShare = m
	saveLocked()
	mu.Unlock()
}
