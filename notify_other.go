//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

func notifyDesktop(title, body string) {
	go func() {
		if runtime.GOOS == "darwin" {
			exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title %q", body, title)).Run()
			return
		}
		exec.Command("notify-send", title, body).Run()
	}()
}
