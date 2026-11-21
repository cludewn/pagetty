package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func cliInput(input *os.File) (*os.File, error) {
	if input == nil || term.IsTerminal(int(input.Fd())) {
		return input, nil
	}
	kind, err := windows.GetFileType(windows.Handle(input.Fd()))
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if kind == windows.FILE_TYPE_CHAR {
		return nil, nil
	}
	return nil, errRedirectedInput
}
