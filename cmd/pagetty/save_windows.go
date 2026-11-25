package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func publishImage(source, target string) error {
	src, err := windows.UTF16PtrFromString(absoluteWindowsPath(source))
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(absoluteWindowsPath(target))
	if err != nil {
		return err
	}
	if err := windows.MoveFile(src, dst); err != nil {
		return &os.LinkError{Op: "publish", Old: source, New: target, Err: err}
	}
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
