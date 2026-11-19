package main

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Live updates for the admin window.
//
// Before, the window asked for the state every 2 s, and the PC noticed a network change only
// every 4-10 s, so things appeared late and then all at once. Now:
//   - netFast checks the adapters every 0.7 s and wakes the other loops the moment they change;
//   - notifyState tells every open admin window "something changed", and the window fetches
//     the new state at once (/admin/events). The 2 s refresh stays as a safety net.

var (
	stMu sync.Mutex
	stCh = make(chan struct{})

	kickNW = make(chan struct{}, 1) // wakes netWatch (the internet check)
)

// notifyState wakes every admin window that is waiting for news.
func notifyState() {
	stMu.Lock()
	close(stCh)
	stCh = make(chan struct{})
	stMu.Unlock()
}

func stateWait() <-chan struct{} {
	stMu.Lock()
	defer stMu.Unlock()
	return stCh
}

// netSig is a short fingerprint of the PC's real network addresses; it changes when Wi-Fi
// is joined or lost, or the address changes.
func netSig() string {
	var parts []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || virtualAdapter(i.Name) {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if v4 := n.IP.To4(); v4 != nil && !v4.IsLinkLocalUnicast() && !v4.IsLoopback() {
					parts = append(parts, i.Name+"="+v4.String())
				}
			}
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",") + "|" + strings.Join(localIPs(), ",")
}

// netFast notices a network change within a second.
func netFast() {
	last := netSig()
	for {
		time.Sleep(700 * time.Millisecond)
		s := netSig()
		if s == last {
			continue
		}
		last = s
		notifyState() // the QR address may already be different
		select {
		case kickNW <- struct{}{}:
		default:
		}
		kickHotspot()
	}
}

// adminEvents is a Server-Sent-Events stream: one tiny message each time something changed.
func adminEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 1000\n\n")
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	ch := stateWait()
	for {
		select {
		case <-ch:
			ch = stateWait()
			fmt.Fprint(w, "data: s\n\n")
		case <-tick.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case <-r.Context().Done():
			return
		}
		fl.Flush()
	}
}
