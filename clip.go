package main

// Clipboard and drag-out routes for the desktop file manager.
// Same rules as adminfiles.go: wrapped in local() (this PC only), mutating routes check the Origin
// header, and every path inside the shared / received folder goes through rootResolve.

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// POST /admin/clipcopy  {root, paths, cut}  ->  put the chosen files on the real clipboard
// (so they can be pasted in Explorer, on the Desktop, in a chat app...).
func adminClipCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !sameOrigin(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var b struct {
		Root  string
		Paths []string
		Cut   bool
	}
	if !adminReadJSON(r, &b) || len(b.Paths) == 0 {
		http.Error(w, "bad request", 400)
		return
	}
	var real []string
	for _, p := range b.Paths {
		sub := cleanSub(p)
		base, full, ok := rootResolve(b.Root, sub)
		if !ok || sub == "" || samePath(full, base) {
			continue
		}
		if _, err := os.Lstat(full); err == nil {
			real = append(real, full)
		}
	}
	if len(real) == 0 {
		http.Error(w, "Nothing to copy", 400)
		return
	}
	if err := setClipboardFiles(real, b.Cut); err != nil {
		// Not fatal: the in-app Copy / Paste still works. The page just skips the "any folder" hint.
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// POST /admin/clippaste  {root, to}  ->  paste whatever files or folders are on the real clipboard
// (copied in Explorer, or copied here) into the folder "to". Replies {ok, none, done, error}.
func adminClipPaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !sameOrigin(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var b struct{ Root, To string }
	if !adminReadJSON(r, &b) {
		http.Error(w, "bad request", 400)
		return
	}
	_, dir, ok := rootResolve(b.Root, cleanSub(b.To))
	if !ok {
		http.Error(w, "Choose a folder first", 400)
		return
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		http.Error(w, "That folder no longer exists", 400)
		return
	}
	files, cut := getClipboardFiles()
	if len(files) == 0 {
		writeJSON(w, map[string]any{"ok": true, "none": true})
		return
	}
	done, firstErr := 0, ""
	for _, src := range files {
		if err := transferOne(filepath.Clean(src), dir, cut, true); err != nil {
			if firstErr == "" {
				firstErr = err.Error()
			}
			continue
		}
		done++
	}
	if cut && done > 0 && firstErr == "" {
		clearClipboard() // the files are no longer where the clipboard says
	}
	writeJSON(w, map[string]any{"ok": firstErr == "", "done": done, "cut": cut, "error": firstErr})
}

// GET /admin/raw/<path>?root=share|recv  ->  the file itself, as a download. This is what the window
// hands to Windows when a file is dragged out of shareUp onto the Desktop or into another folder.
func adminRaw(w http.ResponseWriter, r *http.Request) {
	_, full, ok := rootResolve(r.URL.Query().Get("root"), strings.TrimPrefix(r.URL.Path, "/admin/raw/"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(st.Name()))
	http.ServeContent(w, r, "", st.ModTime(), f)
}
