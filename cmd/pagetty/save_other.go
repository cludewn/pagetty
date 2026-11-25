//go:build !linux && !windows

package main

import "os"

func publishImage(source, target string) error {
	return os.Link(source, target)
}
