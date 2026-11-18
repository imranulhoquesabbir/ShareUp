package main

// Extra admin file-manager actions: rename, move, copy.
// Same rules as adminfiles.go: wrapped in local(), mutating routes check Origin,
// every path goes through rootResolve so nothing can leave the chosen folder.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// samePath compares two real paths (Windows paths are case-insensitive).
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// isInside reports whether child is strictly inside parent.
func isInside(parent, child string) bool {
	parent, child = filepath.Clean(parent), filepath.Clean(child)
	if runtime.GOOS == "windows" {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// uniqueDest returns a path inside dir that does not exist yet:
// "name", then "name (1)", "name (2)" ... (the extension stays at the end for files).
func uniqueDest(dir, name string, isDir bool) string {
	ext, stem := "", name
	if !isDir {
		ext = filepath.Ext(name)
		stem = strings.TrimSuffix(name, ext)
	}
	for i := 0; ; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		d := filepath.Join(dir, n)
		if _, err := os.Lstat(d); os.IsNotExist(err) {
			return d
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	os.Chtimes(dst, st.ModTime(), st.ModTime())
	return nil
}

// copyTree copies a file or a whole folder. Shortcuts / symlinks and special files are skipped.
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.IsDir() {
		if err := os.MkdirAll(dst, 0755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if st.Mode().IsRegular() {
		return copyFile(src, dst)
	}
	return nil
}

// transferOne moves or copies src into the folder dir. It never overwrites:
// if the name is taken the new one becomes "name (1)".
func transferOne(src, dir string, move, crossRoot bool) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	isDir := st.IsDir()
	if isDir && (samePath(src, dir) || isInside(src, dir)) {
		return errors.New("A folder can't be put inside itself")
	}
	if move && samePath(filepath.Dir(src), dir) {
		return nil // already there
	}
	dst := uniqueDest(dir, filepath.Base(src), isDir)
	if move {
		err := os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if !crossRoot {
			return err
		}
		// different drives: fall back to copy + delete
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	if move {
		return os.RemoveAll(src)
	}
	return nil
}

// POST /admin/move  or  /admin/copy
//
//	{root, paths: ["a/b.txt", "c"], to: "folder/sub", toRoot}
//
// toRoot is optional (default = root). Replies {ok, done, error}.
func adminTransfer(move bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !sameOrigin(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		var b struct {
			Root, ToRoot, To string
			Paths            []string
		}
		if !adminReadJSON(r, &b) || len(b.Paths) == 0 {
			http.Error(w, "bad request", 400)
			return
		}
		if b.ToRoot == "" {
			b.ToRoot = b.Root
		}
		_, dir, ok := rootResolve(b.ToRoot, cleanSub(b.To))
		if !ok {
			http.Error(w, "Choose a folder first", 400)
			return
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			http.Error(w, "That folder no longer exists", 400)
			return
		}
		crossRoot := !samePath(rootDir(b.Root), rootDir(b.ToRoot))
		done, firstErr := 0, ""
		for _, p := range b.Paths {
			sub := cleanSub(p)
			base, src, ok := rootResolve(b.Root, sub)
			if !ok || sub == "" || samePath(src, base) {
				continue
			}
			if err := transferOne(src, dir, move, crossRoot); err != nil {
				if firstErr == "" {
					firstErr = err.Error()
				}
				continue
			}
			done++
		}
		writeJSON(w, map[string]any{"ok": firstErr == "", "done": done, "error": firstErr})
	}
}

// POST /admin/rename  {root, path, name}  ->  rename a file or folder in place
func adminRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !sameOrigin(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var b struct{ Root, Path, Name string }
	if !adminReadJSON(r, &b) {
		http.Error(w, "bad request", 400)
		return
	}
	sub := cleanSub(b.Path)
	base, src, ok := rootResolve(b.Root, sub)
	if !ok || sub == "" || samePath(src, base) {
		http.Error(w, "forbidden", 403)
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		http.Error(w, "Type a name first", 400)
		return
	}
	name = cleanName(name)
	if strings.HasPrefix(name, ".") {
		http.Error(w, "A name can't start with a dot", 400)
		return
	}
	dst := filepath.Join(filepath.Dir(src), name)
	if filepath.Base(src) == name {
		writeJSON(w, map[string]any{"ok": true, "name": name})
		return
	}
	// A different name that already exists is refused. A change of letter case only
	// ("a.txt" -> "A.txt") looks like the same file on Windows, so it is let through.
	if _, err := os.Lstat(dst); err == nil && !strings.EqualFold(filepath.Base(src), name) {
		http.Error(w, "Something with that name already exists", 409)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}
