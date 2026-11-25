package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func (img *stagedImage) publish(target string) (err error) {
	src, err := windows.UTF16PtrFromString(absoluteWindowsPath(img.path))
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(absoluteWindowsPath(target))
	if err != nil {
		return err
	}
	// FATのファイルIDは移動で変わるため、取り消し時も同じハンドルから取得する。
	handle, err := windows.CreateFile(src, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: img.path, Err: err}
	}
	file := os.NewFile(uintptr(handle), img.path)
	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, img.info) {
		img.replaced = true
		return fmt.Errorf("一時画像が置き換えられたため公開できません: %s", img.path)
	}
	if err := windows.MoveFile(src, dst); err != nil {
		return &os.LinkError{Op: "publish", Old: img.path, New: target, Err: err}
	}
	img.file = file
	return nil
}

// Win32の公開APIでも長いパスを扱えるようにする。
func absoluteWindowsPath(path string) string {
	if len(path) < 248 || strings.HasPrefix(path, "\\\\?\\") || strings.HasPrefix(path, "\\\\.\\") {
		return path
	}
	if rest, ok := strings.CutPrefix(path, "\\\\"); ok {
		return "\\\\?\\UNC\\" + rest
	}
	return "\\\\?\\" + path
}
