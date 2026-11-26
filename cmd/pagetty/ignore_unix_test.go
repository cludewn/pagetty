//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

func ignoreTestInterrupt() error {
	signal.Ignore(os.Interrupt)
	return nil
}

func interruptTestChild() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		os.Exit(17)
	}
	time.Sleep(time.Minute)
}
