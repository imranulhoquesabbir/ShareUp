//go:build windows

package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
	"github.com/jchv/go-webview2"
)

//go:embed icon.ico
var trayIcon []byte

var (
	user32         = syscall.NewLazyDLL("user32.dll")
	pShowWindow    = user32.NewProc("ShowWindow")
	pSetForeground = user32.NewProc("SetForegroundWindow")
	pIsIconic      = user32.NewProc("IsIconic")
	pSysParams     = user32.NewProc("SystemParametersInfoW")
	winMu          sync.Mutex
	winHwnd        uintptr // 0 while the window is closed (app lives in the tray)
	showCh         = make(chan struct{}, 1)
	updItem        *systray.MenuItem
)

const (
	swRestore = 9
	swShow    = 5
)

// uiShow opens the window, or brings it to the front if it is already open.
func uiShow() {
	kickUpdate()
	winMu.Lock()
	h := winHwnd
	winMu.Unlock()
	if h != 0 {
		if r, _, _ := pIsIconic.Call(h); r != 0 {
			pShowWindow.Call(h, swRestore)
		} else {
			pShowWindow.Call(h, swShow)
		}
		pSetForeground.Call(h)
		return
	}
	select {
	case showCh <- struct{}{}:
	default:
	}
}

// uiQuit takes the tray icon away. Exiting the process without this leaves a dead icon in
// the tray until the mouse moves over it (and a second one appears after an update).
func uiQuit() {
	systray.Quit()
	time.Sleep(350 * time.Millisecond) // let Windows really remove the icon
}

func windowOpen() bool {
	winMu.Lock()
	defer winMu.Unlock()
	return winHwnd != 0
}

// windowLoop owns one OS thread (WebView2 needs that). Closing the window only
// destroys the view; the app keeps running in the tray until "Quit".
func windowLoop() {
	runtime.LockOSThread()
	for range showCh {
		openWindow()
	}
}

// windowSize picks a window that fits this screen: up to 1100x820, otherwise 92% x 88% of
// the usable screen area (the part not covered by the taskbar).
func windowSize() (int, int) {
	var rc struct{ l, t, r, b int32 }
	w, h := 1100, 820
	if ok, _, _ := pSysParams.Call(0x0030, 0, uintptr(unsafe.Pointer(&rc)), 0); ok != 0 {
		sw, sh := int(rc.r-rc.l), int(rc.b-rc.t)
		if sw > 0 && sh > 0 {
			if v := sw * 92 / 100; v < w {
				w = v
			}
			if v := sh * 88 / 100; v < h {
				h = v
			}
		}
	}
	if w < 420 {
		w = 420
	}
	if h < 520 {
		h = 520
	}
	return w, h
}

func openWindow() {
	ww, wh := windowSize()
	data := filepath.Join(os.Getenv("LOCALAPPDATA"), "shareUp", "webview")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  data,
		WindowOptions: webview2.WindowOptions{
			Title:  "shareUp",
			Width:  uint(ww),
			Height: uint(wh),
			IconId: 2, // the icon embedded in the exe by the build
			Center: true,
		},
	})
	if w == nil { // WebView2 runtime missing: fall back to the browser
		openBrowser(adminURL())
		return
	}
	defer w.Destroy()
	w.SetSize(420, 520, webview2.HintMin) // resizable, with a sensible minimum
	winMu.Lock()
	winHwnd = uintptr(w.Window())
	winMu.Unlock()
	w.Navigate(adminURL())
	w.Run()
	winMu.Lock()
	winHwnd = 0
	winMu.Unlock()
}

func runUI(silent bool) {
	go windowLoop()
	if !silent {
		uiShow()
	}
	uiUpdateHook = func(tag string) {
		if updItem != nil {
			updItem.SetTitle("Update available: " + tag)
			updItem.Show()
		}
		systray.SetTooltip("shareUp – update available (" + tag + ")")
	}
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("shareUp")
		systray.SetTooltip("shareUp")
		systray.SetOnTapped(uiShow) // left-click the tray icon opens the window
		open := systray.AddMenuItem("Open shareUp", "Show the window")
		updItem = systray.AddMenuItem("", "Open shareUp to install the update")
		updItem.Hide()
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit shareUp", "Stop shareUp")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					uiShow()
				case <-updItem.ClickedCh:
					uiShow()
				case <-quit.ClickedCh:
					quitApp()
				}
			}
		}()
		if u := updateSnapshot(); u.Available {
			uiUpdateHook(u.Latest)
		}
	}, nil)
}
