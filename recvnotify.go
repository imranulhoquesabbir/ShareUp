package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// "2 PDFs, 1 ZIP received": files that arrive together are counted by kind and announced once,
// as a desktop notification and as a message in the shareUp window.

var (
	rvMu     sync.Mutex
	rvCounts = map[string]int{}
	rvFrom   string
	rvFirst  time.Time
	rvTimer  *time.Timer
	rvSeq    int
	rvText   string
	rvKinds  = map[string]string{}
)

func init() {
	for k, exts := range map[string]string{
		"image": "jpg jpeg png gif webp bmp heic heif avif svg",
		"video": "mp4 mkv mov avi webm m4v 3gp",
		"audio": "mp3 wav flac m4a aac ogg opus",
		"pdf":   "pdf",
		"zip":   "zip rar 7z tar gz",
		"word":  "doc docx odt rtf",
		"excel": "xls xlsx csv ods",
		"slide": "ppt pptx odp",
		"text":  "txt md log json xml",
		"app":   "apk exe msi dmg",
	} {
		for _, e := range strings.Fields(exts) {
			rvKinds[e] = k
		}
	}
}

var rvOrder = []string{"image", "video", "pdf", "zip", "word", "excel", "slide", "audio", "text", "app", "file"}

// names: English one, English many, Bangla
var rvNames = map[string][3]string{
	"image": {"photo", "photos", "ছবি"},
	"video": {"video", "videos", "Video"},
	"pdf":   {"PDF", "PDFs", "PDF"},
	"zip":   {"ZIP", "ZIPs", "ZIP"},
	"word":  {"Word file", "Word files", "Word"},
	"excel": {"Excel file", "Excel files", "Excel"},
	"slide": {"slide file", "slide files", "Slides"},
	"audio": {"audio file", "audio files", "Audio"},
	"text":  {"text file", "text files", "Text"},
	"app":   {"app file", "app files", "App"},
	"file":  {"file", "files", "File"},
}

var bnDigits = strings.NewReplacer("0", "০", "1", "১", "2", "২", "3", "৩", "4", "৪", "5", "৫", "6", "৬", "7", "৭", "8", "৮", "9", "৯")

func recvKind(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if k, ok := rvKinds[ext]; ok {
		return k
	}
	return "file"
}

func recvBody(counts map[string]int, lang string) string {
	var parts []string
	for _, k := range rvOrder {
		n := counts[k]
		if n == 0 {
			continue
		}
		nm := rvNames[k]
		if lang == "en" {
			w := nm[1]
			if n == 1 {
				w = nm[0]
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, w))
		} else {
			parts = append(parts, bnDigits.Replace(fmt.Sprintf("%d", n))+"টি "+nm[2])
		}
	}
	if lang == "en" {
		return "Received " + strings.Join(parts, ", ")
	}
	return strings.Join(parts, ", ") + " পেয়েছেন"
}

// noteReceived is called once for every file that finished arriving.
func noteReceived(name, from string) {
	k := recvKind(name)
	rvMu.Lock()
	if len(rvCounts) == 0 {
		rvFirst = time.Now()
	}
	rvCounts[k]++
	rvFrom = from
	if rvTimer != nil {
		rvTimer.Stop()
	}
	wait := 2500 * time.Millisecond // wait for the rest of the batch
	if time.Since(rvFirst) > 20*time.Second {
		wait = 0 // a long batch: do not keep the person waiting for the end
	}
	rvTimer = time.AfterFunc(wait, flushRecv)
	rvMu.Unlock()
}

func flushRecv() {
	rvMu.Lock()
	counts, from := rvCounts, rvFrom
	rvCounts = map[string]int{}
	rvMu.Unlock()
	if len(counts) == 0 {
		return
	}
	lang := get().Lang
	body := recvBody(counts, lang)
	title := "shareUp"
	if from != "" {
		title += " · " + from
	}
	rvMu.Lock()
	rvSeq++
	rvText = body
	if from != "" {
		rvText = from + ": " + body
	}
	rvMu.Unlock()
	notifyDesktop(title, body)
	notifyState()
}

func recvNote() map[string]any {
	rvMu.Lock()
	defer rvMu.Unlock()
	return map[string]any{"n": rvSeq, "t": rvText}
}

// senderName is the name of the phone that made this request (as shown in the window).
func senderName(r *http.Request) string {
	dmu.Lock()
	defer dmu.Unlock()
	s := sessionFrom(r)
	if s == nil {
		return ""
	}
	return shownName(get(), s.DevID, s.Name)
}
