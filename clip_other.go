//go:build !windows

// Copyright (c) 2026 Imranul Hoque Sabbir. All rights reserved.

package main

import "errors"

// The real file clipboard is Windows-only. On other systems the file manager keeps using its own
// in-app Copy / Cut / Paste.

func setClipboardFiles(paths []string, cut bool) error {
	return errors.New("not supported on this system")
}

func getClipboardFiles() (files []string, cut bool) { return nil, false }

func clearClipboard() {}
