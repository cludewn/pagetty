package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func TestCLIWindowsConsoleChild(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "pagetty-cli-console" {
		return
	}
	mode, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		os.Exit(10)
	}
	timeout := 15 * time.Second
	if mode == "deadline" {
		timeout = 500 * time.Millisecond
	}
	if mode == "ctrlc" || mode == "deadline" {
		mode = "block"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	code := runCLI(ctx, cliArgs([]string{"--output-dir", dir}, mode), os.Stdin, os.Stdout, os.Stderr)
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "CLI_CONTEXT_ERROR:%v\n", err)
		os.Exit(11)
	}
	after, err := term.GetState(int(os.Stdin.Fd()))
	fmt.Printf("\nCLI_RESULT:%d:restored=%t\n", code, err == nil && reflect.DeepEqual(before, after))
	os.Exit(0)
}

func TestCLIWindowsInteractionAndCtrlC(t *testing.T) {
	for _, mode := range []string{"interactive", "ctrlc", "size", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			p, err := xpty.NewConPty(200, 12)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			var handle windows.Handle
			process := windows.CurrentProcess()
			if err := windows.DuplicateHandle(process, windows.Handle(p.OutPipeReadFd()), process, &handle, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
				t.Fatal(err)
			}
			reader := os.NewFile(uintptr(handle), "console-output")
			defer reader.Close()
			dir := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			args := []string{exe, "-test.run=^TestCLIWindowsConsoleChild$", "--", "pagetty-cli-console", mode, dir}
			pid, childHandle, err := p.Spawn(exe, args, &syscall.ProcAttr{Env: os.Environ()})
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(windows.Handle(childHandle))
			child, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			defer child.Kill()
			out := &consoleResponder{input: p}
			wantCode := 0
			if mode == "interactive" {
				out.responses = []consoleResponse{{"input0:", "日本語\r"}, {"input1:", "second\r"}}
			} else if mode == "ctrlc" {
				wantCode = 130
				out.responses = []consoleResponse{{"ready", "\x03"}}
			}
			readDone := make(chan error, 1)
			go func() { _, err := io.Copy(out, reader); readDone <- err }()
			waitErr := xpty.WaitProcess(ctx, &exec.Cmd{Process: child})
			closeErr := p.Close()
			readErr := <-readDone
			if mode == "deadline" {
				var exitErr *exec.ExitError
				if ctx.Err() != nil || closeErr != nil || readErr != nil || !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 11 || !strings.Contains(out.String(), "CLI_CONTEXT_ERROR:context deadline exceeded") || strings.Contains(out.String(), "CLI_RESULT:") {
					t.Fatalf("deadline accepted: wait=%v close=%v read=%v output=%q", waitErr, closeErr, readErr, out.String())
				}
				return
			}
			if waitErr != nil || closeErr != nil || readErr != nil || len(out.responses) != 0 || !strings.Contains(out.String(), fmt.Sprintf("CLI_RESULT:%d:restored=true", wantCode)) {
				t.Fatalf("wait=%v close=%v read=%v output=%q", waitErr, closeErr, readErr, out.String())
			}
			if mode == "interactive" && (!strings.Contains(out.String(), "got:日本語") || !strings.Contains(out.String(), "got:second")) {
				t.Fatalf("input not forwarded: %q", out.String())
			}
			if mode == "size" && !strings.Contains(out.String(), "size:100:12:<nil>") {
				t.Fatalf("host terminal rows not used: %q", out.String())
			}
			paths := imageFiles(t, dir, "png")
			if len(paths) != 1 || (mode == "ctrlc" && !strings.Contains(paths[0], "-partial_001")) {
				t.Fatalf("paths=%v output=%q", paths, out.String())
			}
			readImage(t, paths[0])
		})
	}
}
