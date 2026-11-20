package main

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	toastMu   sync.Mutex
	toastLast time.Time
)

// notifyDesktop shows a normal Windows notification (bottom-right corner / notification centre).
func notifyDesktop(title, body string) {
	// Notices that come close together share one tag: the newer one replaces the older one in the
	// notification centre, and does not pop up a second banner on screen.
	toastMu.Lock()
	quiet := "0"
	if time.Since(toastLast) < 10*time.Second {
		quiet = "1"
	}
	toastLast = time.Now()
	toastMu.Unlock()
	go func() {
		script := `$ErrorActionPreference='Stop'
[void][Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
[void][Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]
$t=[Security.SecurityElement]::Escape($env:SU_T);$b=[Security.SecurityElement]::Escape($env:SU_B)
$x=New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml("<toast><visual><binding template='ToastGeneric'><text>$t</text><text>$b</text></binding></visual></toast>")
$n=[Windows.UI.Notifications.ToastNotification]::new($x)
$n.Tag='su-recv';$n.Group='shareUp'
if($env:SU_Q -eq '1'){$n.SuppressPopup=$true}
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($n)`
		c := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
		c.Env = append(os.Environ(), "SU_T="+title, "SU_B="+body, "SU_Q="+quiet)
		hideWindow(c)
		c.Run()
	}()
}
