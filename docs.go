package main

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

// Document kinds shown as separate tabs on the phone.
var docKinds = map[string]string{
	".pdf": "pdf",
	".doc": "word", ".docx": "word", ".odt": "word", ".rtf": "word", ".pages": "word",
	".xls": "sheet", ".xlsx": "sheet", ".csv": "sheet", ".ods": "sheet", ".numbers": "sheet",
	".ppt": "slide", ".pptx": "slide", ".odp": "slide", ".key": "slide",
	".txt": "text", ".md": "text", ".log": "text", ".json": "text", ".xml": "text",
}

// docs lists the documents (PDF, Word, Excel, slides, text) inside the shared folder,
// newest first, so the phone can show them under one "Documents" button.
func docs(w http.ResponseWriter, r *http.Request) {
	c := get()
	type item struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		T    int64  `json:"t"`
		K    string `json:"k"`
	}
	out := []item{}
	on := c.ShareOn && c.ShareDir != ""
	if on {
		base := filepath.Clean(c.ShareDir)
		filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if p != base && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel != "." && strings.Count(rel, "/") >= 6 {
					return filepath.SkipDir
				}
				return nil
			}
			k := docKinds[strings.ToLower(filepath.Ext(d.Name()))]
			if k == "" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, item{rel, info.Size(), info.ModTime().Unix(), k})
			if len(out) >= 5000 {
				return filepath.SkipAll
			}
			return nil
		})
		sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	}
	writeJSON(w, map[string]any{"shareOn": on, "items": out})
}
