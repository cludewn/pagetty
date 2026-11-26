package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	options := []string{"--output-dir", dir}
	if strings.HasPrefix(mode, "catch-") || mode == "ignore-repeat" {
		options = append(options, "-s")
	}
	if strings.HasPrefix(mode, "limit-") {
		options = append(options, "--max-lines", "1", "--page-limit", "2", "--format", "svg")
	}
	code := runCLI(ctx, cliArgs(options, mode), os.Stdin, os.Stdout, os.Stderr)
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "CLI_CONTEXT_ERROR:%v\n", err)
		os.Exit(11)
	}
	after, err := term.GetState(int(os.Stdin.Fd()))
	fmt.Printf("\nCLI_RESULT:%d:restored=%t\n", code, err == nil && reflect.DeepEqual(before, after))
	os.Exit(0)
}

func TestCLIWindowsInteractionAndCtrlC(t *testing.T) {
	modes := []string{"interactive", "ctrlc", "size", "catch-exit", "catch-twice", "ignore-repeat", "limit-input", "limit-interrupt", "deadline"}
	for _, tc := range pagePromptCases {
		modes = append(modes, tc.mode)
	}
	for _, mode := range modes {
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
			var promptCase *pagePromptCase
			for _, tc := range pagePromptCases {
				if mode == tc.mode {
					promptCase = &tc
					if err := os.WriteFile(filepath.Join(dir, "existing.png"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
					args = []string{exe, "-test.run=^TestCLIPagePromptProcess$", "--", "pagetty-page-prompt", mode, dir}
					break
				}
			}
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
			if promptCase != nil {
				out.responses = promptCase.responses("\x1a\r")
			} else if mode == "interactive" {
				out.responses = []consoleResponse{{"input0:", "日本語\r"}, {"input1:", "second\r"}}
			} else if mode == "ctrlc" {
				wantCode = 130
				out.responses = []consoleResponse{{"ready", "\x03"}}
			} else if mode == "catch-exit" {
				wantCode = 7
				out.responses = []consoleResponse{{"ready", "\x03"}}
			} else if mode == "catch-twice" {
				wantCode = 7
				out.responses = []consoleResponse{{"ready", "\x03"}, {"first interrupt handled", "\x03"}}
			} else if mode == "ignore-repeat" {
				out.responses = []consoleResponse{
					{"ready", "\x03"}, {"first interrupt ignored", "\x1b[67;46;99;1;8;1_\x1b[67;46;99;1;8;2_\x1b[67;46;99;0;8;1_"}, {"still running>", "continue\r"},
				}
			} else if mode == "limit-input" {
				wantCode = 1
				out.responses = []consoleResponse{{"post-limit>", "still interactive\r"}, {"選択 [a/l/N]:", "l\r"}}
			} else if mode == "limit-interrupt" {
				wantCode = 130
				out.responses = []consoleResponse{{"post-limit>", "\x03"}}
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
			if promptCase != nil {
				if waitErr != nil || closeErr != nil || readErr != nil {
					t.Fatalf("wait=%v close=%v read=%v output=%q", waitErr, closeErr, readErr, out.String())
				}
				promptCase.check(t, dir, out)
				return
			}
			if waitErr != nil || closeErr != nil || readErr != nil || len(out.responses) != 0 || !strings.Contains(out.String(), fmt.Sprintf("CLI_RESULT:%d:restored=true", wantCode)) {
				t.Fatalf("wait=%v close=%v read=%v output=%q", waitErr, closeErr, readErr, out.String())
			}
			if mode == "interactive" && (!strings.Contains(out.String(), "got:日本語") || !strings.Contains(out.String(), "got:second")) {
				t.Fatalf("input not forwarded: %q", out.String())
			}
			if mode == "size" && !strings.Contains(out.String(), "size:80:12:<nil>") {
				t.Fatalf("host terminal rows not used: %q", out.String())
			}
			extension, expected := "png", 1
			if wantCode == 130 {
				expected = 0
			}
			if strings.HasPrefix(mode, "limit-") {
				extension = "svg"
			}
			if mode == "limit-input" {
				expected = 2
				if !strings.Contains(out.String(), "received:still interactive") {
					t.Fatalf("input after limit lost: %q", out.String())
				}
			}
			if mode == "catch-twice" && !strings.Contains(out.String(), "second interrupt handled") {
				t.Fatalf("second key was not forwarded: %q", out.String())
			}
			if mode == "ignore-repeat" && !strings.Contains(out.String(), "continued after interrupts") {
				t.Fatalf("target did not continue after repeated input: %q", out.String())
			}
			paths := imageFiles(t, dir, extension)
			if len(paths) != expected {
				t.Fatalf("paths=%v output=%q", paths, out.String())
			}
			for _, path := range paths {
				readImage(t, path)
				if (strings.HasPrefix(mode, "catch-") || mode == "ignore-repeat") && strings.Contains(path, "-partial_") {
					t.Fatalf("ordinary exit marked partial: %s", path)
				}
			}
		})
	}
}

func ignoreTestInterrupt() error {
	setHandler := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
	ok, _, err := setHandler.Call(0, 1)
	if ok == 0 {
		return err
	}
	return nil
}

func interruptTestChild() {
	code := uint32(windows.STATUS_CONTROL_C_EXIT)
	os.Exit(int(code))
}
