//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/term"
)

func cliInput(input *os.File) (*os.File, error) {
	if input == nil || term.IsTerminal(int(input.Fd())) {
		return input, nil
	}
	info, err := input.Stat()
	if errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EBADF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		null, err := os.Stat(os.DevNull)
		if err != nil {
			return nil, err
		}
		if os.SameFile(info, null) {
			return nil, nil
		}
	}
	return nil, errRedirectedInput
}
