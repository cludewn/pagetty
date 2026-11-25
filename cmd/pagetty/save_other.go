//go:build !linux && !windows

package main

import "os"

func (img *stagedImage) publish(target string) error {
	return os.Link(img.path, target)
}
