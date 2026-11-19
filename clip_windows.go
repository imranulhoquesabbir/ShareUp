//go:build windows

package main

// Real Windows clipboard support for the file manager.
//
// The page inside the window can only copy text to the clipboard. To make "Copy" / "Cut" on files
// work like File Explorer (paste into any folder, Desktop, WhatsApp, e-mail...), the files are put on
// the clipboard here as a CF_HDROP list plus the "Preferred DropEffect" flag (copy or move).
// The same two calls read that list back, so files copied in Explorer can be pasted into shareUp
// (folders included).

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	pOpenClipboard      = user32.NewProc("OpenClipboard")
	pCloseClipboard     = user32.NewProc("CloseClipboard")
	pEmptyClipboard     = user32.NewProc("EmptyClipboard")
	pSetClipboardData   = user32.NewProc("SetClipboardData")
	pGetClipboardData   = user32.NewProc("GetClipboardData")
	pIsClipFormat       = user32.NewProc("IsClipboardFormatAvailable")
	pRegisterClipFormat = user32.NewProc("RegisterClipboardFormatW")
	pGlobalAlloc        = kernel32.NewProc("GlobalAlloc")
	pGlobalFree         = kernel32.NewProc("GlobalFree")
	pGlobalLock         = kernel32.NewProc("GlobalLock")
	pGlobalUnlock       = kernel32.NewProc("GlobalUnlock")
	pDragQueryFile      = shell32.NewProc("DragQueryFileW")
)

const (
	cfHDrop       = 15
	gmemMoveable  = 0x0002
	gmemZeroInit  = 0x0040
	dropEffectCpy = 1
	dropEffectMov = 2
)

func openClip() error {
	for i := 0; i < 15; i++ { // another program may hold the clipboard for a moment
		if r, _, _ := pOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("The clipboard is busy. Try again.")
}

// ptr turns a Windows memory address into something Go lets us read and write.
func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// globalCopy puts b into new movable global memory (what the clipboard wants).
func globalCopy(b []byte) (uintptr, error) {
	h, _, _ := pGlobalAlloc.Call(gmemMoveable|gmemZeroInit, uintptr(len(b)))
	if h == 0 {
		return 0, errors.New("Out of memory")
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return 0, errors.New("Out of memory")
	}
	copy(unsafe.Slice((*byte)(ptr(p)), len(b)), b)
	pGlobalUnlock.Call(h)
	return h, nil
}

func clipFormat(name string) uintptr {
	n, _ := syscall.UTF16PtrFromString(name)
	f, _, _ := pRegisterClipFormat.Call(uintptr(unsafe.Pointer(n)))
	return f
}

// setClipboardFiles replaces the clipboard with these files. cut = true makes Explorer MOVE them on paste.
func setClipboardFiles(paths []string, cut bool) error {
	if len(paths) == 0 {
		return errors.New("Nothing to copy")
	}
	// DROPFILES header: offset to names, POINT (8 bytes), fNC, fWide = 20 bytes, then UTF-16 names.
	buf := make([]byte, 20)
	binary.LittleEndian.PutUint32(buf[0:], 20)
	binary.LittleEndian.PutUint32(buf[16:], 1)
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		for _, u := range utf16.Encode([]rune(abs)) {
			buf = binary.LittleEndian.AppendUint16(buf, u)
		}
		buf = append(buf, 0, 0)
	}
	buf = append(buf, 0, 0) // list ends with an empty name

	hDrop, err := globalCopy(buf)
	if err != nil {
		return err
	}
	eff := uint32(dropEffectCpy)
	if cut {
		eff = dropEffectMov
	}
	var ebuf [4]byte
	binary.LittleEndian.PutUint32(ebuf[:], eff)
	hEff, err := globalCopy(ebuf[:])
	if err != nil {
		pGlobalFree.Call(hDrop)
		return err
	}

	if err := openClip(); err != nil {
		pGlobalFree.Call(hDrop)
		pGlobalFree.Call(hEff)
		return err
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	if r, _, _ := pSetClipboardData.Call(cfHDrop, hDrop); r == 0 {
		pGlobalFree.Call(hDrop)
		pGlobalFree.Call(hEff)
		return errors.New("Could not copy to the clipboard")
	}
	if f := clipFormat("Preferred DropEffect"); f == 0 {
		pGlobalFree.Call(hEff)
	} else if r, _, _ := pSetClipboardData.Call(f, hEff); r == 0 {
		pGlobalFree.Call(hEff)
	}
	return nil
}

// getClipboardFiles returns the files (and folders) on the clipboard, and whether they were cut.
func getClipboardFiles() (files []string, cut bool) {
	if r, _, _ := pIsClipFormat.Call(cfHDrop); r == 0 {
		return nil, false
	}
	if openClip() != nil {
		return nil, false
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(cfHDrop)
	if h == 0 {
		return nil, false
	}
	n, _, _ := pDragQueryFile.Call(h, 0xFFFFFFFF, 0, 0)
	for i := uintptr(0); i < n && i < 5000; i++ {
		l, _, _ := pDragQueryFile.Call(h, i, 0, 0)
		if l == 0 {
			continue
		}
		b := make([]uint16, l+1)
		pDragQueryFile.Call(h, i, uintptr(unsafe.Pointer(&b[0])), l+1)
		files = append(files, syscall.UTF16ToString(b))
	}
	if f := clipFormat("Preferred DropEffect"); f != 0 {
		if r, _, _ := pIsClipFormat.Call(f); r != 0 {
			if he, _, _ := pGetClipboardData.Call(f); he != 0 {
				if p, _, _ := pGlobalLock.Call(he); p != 0 {
					cut = *(*uint32)(ptr(p))&dropEffectMov != 0 && *(*uint32)(ptr(p))&dropEffectCpy == 0
					pGlobalUnlock.Call(he)
				}
			}
		}
	}
	return files, cut
}

// clearClipboard empties it (after a "cut" has been pasted, like Explorer does).
func clearClipboard() {
	if openClip() == nil {
		pEmptyClipboard.Call()
		pCloseClipboard.Call()
	}
}
