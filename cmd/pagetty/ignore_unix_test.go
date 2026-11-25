//go:build !windows

package main

import (
	"os"
	"os/signal"
)

func ignoreTestInterrupt() error {
	signal.Ignore(os.Interrupt)
	return nil
}
