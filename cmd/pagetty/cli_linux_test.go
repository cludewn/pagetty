package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestCLIUnixInteractionAndCtrlC(t *testing.T) {
	for _, mode := range []string{"interactive", "ctrlc", "size", "raw-key", "catch-exit", "catch-twice", "ignore-repeat", "limit-input", "limit-interrupt"} {
		t.Run(mode, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(slave, &pty.Winsize{Cols: 80, Rows: 12}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			out := &consoleResponder{input: master}
			wantCode := 0
			childMode := mode
			if mode == "interactive" {
				out.responses = []consoleResponse{{"input0:", "日本語\r"}, {"input1:", "second\r"}}
			} else if mode == "ctrlc" {
				childMode, wantCode = "block", 130
				out.responses = []consoleResponse{{"ready", "\x03"}}
			} else if mode == "catch-exit" {
				wantCode = 7
				out.responses = []consoleResponse{{"ready", "\x03"}}
			} else if mode == "catch-twice" {
				wantCode = 7
				out.responses = []consoleResponse{{"ready", "\x03"}, {"first interrupt handled", "\x03"}}
			} else if mode == "raw-key" {
				out.responses = []consoleResponse{{"ready", "\x03"}}
			} else if mode == "ignore-repeat" {
				out.responses = []consoleResponse{
					{"ready", "\x03"}, {"first interrupt ignored", "\x03\x03"}, {"still running>", "continue\r"},
				}
			} else if mode == "limit-input" {
				wantCode = 1
				out.responses = []consoleResponse{{"post-limit>", "still interactive\r"}}
			} else if mode == "limit-interrupt" {
				wantCode = 130
				out.responses = []consoleResponse{{"post-limit>", "\x03"}}
			}
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var diagnostics bytes.Buffer
			options := []string{"--output-dir", dir}
			if strings.HasPrefix(mode, "catch-") || mode == "ignore-repeat" || mode == "raw-key" {
				options = append(options, "-s")
			}
			if strings.HasPrefix(mode, "limit-") {
				options = append(options, "--max-lines", "1", "--page-limit", "2", "--format", "svg")
			}
			code := runCLI(ctx, cliArgs(options, childMode), slave, out, &diagnostics)
			if code != wantCode || len(out.responses) != 0 {
				t.Fatalf("code=%d output=%q diagnostics=%q", code, out.String(), diagnostics.String())
			}
			after, err := term.GetState(int(slave.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("terminal not restored: %v", err)
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
			if mode == "raw-key" && !strings.Contains(out.String(), "raw key received") {
				t.Fatalf("raw key lost: %q", out.String())
			}
			if mode == "catch-twice" && !strings.Contains(out.String(), "second interrupt handled") {
				t.Fatalf("second key was not forwarded: %q", out.String())
			}
			if mode == "ignore-repeat" && !strings.Contains(out.String(), "continued after interrupts") {
				t.Fatalf("target did not continue after repeated input: %q", out.String())
			}
			paths := imageFiles(t, dir, extension)
			if len(paths) != expected {
				t.Fatalf("paths=%v diagnostics=%q", paths, diagnostics.String())
			}
			for _, path := range paths {
				readImage(t, path)
				if (strings.HasPrefix(mode, "catch-") || mode == "ignore-repeat" || mode == "raw-key") && strings.Contains(path, "-partial_") {
					t.Fatalf("ordinary exit marked partial: %s", path)
				}
			}
		})
	}
}

func TestCLIUnixSignalExit(t *testing.T) {
	dir := t.TempDir()
	code, _, diagnostics := runTestCLI(t, []string{"--output-dir", dir, "--", "/bin/sh", "-c", "printf before; kill -KILL $$"})
	if code != 1 || !strings.Contains(diagnostics, "終了コードを取得できません") || len(imageFiles(t, dir, "png")) != 1 {
		t.Fatalf("code=%d diagnostics=%q", code, diagnostics)
	}
}

func TestCLIUnixProcessSignals(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "pagetty")
	buildCtx, stopBuild := context.WithTimeout(context.Background(), time.Minute)
	defer stopBuild()
	if output, err := exec.CommandContext(buildCtx, "go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	for _, mode := range []string{"normal", "child SIGPIPE", "closed stdout", "Ctrl+C"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			maxLines := "40"
			if mode == "closed stdout" {
				maxLines = "1"
			}
			args := []string{"--columns", "20", "--max-lines", maxLines, "--output-dir", dir, "--", "/bin/sh", "-c"}
			script := "printf 'ready\\n'"
			wantCode, wantPages := 0, 1
			if mode == "child SIGPIPE" {
				script += "; kill -PIPE $$; printf survived"
				wantCode = 1
			} else if mode == "closed stdout" {
				script += "; read token < \"$1\"; printf 'after\\n'"
				wantCode, wantPages = 1, 2
			} else if mode == "Ctrl+C" {
				script += "; read token"
				wantCode, wantPages = 130, 0
			}
			args = append(args, script)
			var gate *os.File
			if mode == "closed stdout" {
				path := filepath.Join(t.TempDir(), "gate")
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				gate, err = os.OpenFile(path, os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _, _ = io.WriteString(gate, "continue\n"); _ = gate.Close() }()
				args = append(args, "child", path)
			}
			cmd := exec.CommandContext(ctx, exe, args...)
			var output, diagnostics bytes.Buffer
			cmd.Stderr = &diagnostics
			var runErr error
			switch mode {
			case "closed stdout", "Ctrl+C":
				var reader io.ReadCloser
				var err error
				if mode == "Ctrl+C" {
					cmd.Stderr = nil
					reader, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
				} else {
					reader, err = cmd.StdoutPipe()
					if err == nil {
						err = cmd.Start()
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				ready := make(chan string, 1)
				go func() {
					lines := bufio.NewReader(reader)
					line, _ := lines.ReadString('\n')
					if mode == "Ctrl+C" && strings.HasPrefix(line, "pagetty: ") {
						line, _ = lines.ReadString('\n')
					}
					ready <- line
				}()
				select {
				case line := <-ready:
					if strings.TrimSpace(line) != "ready" {
						t.Fatalf("first line=%q", line)
					}
				case <-ctx.Done():
					t.Fatal("CLI output timeout")
				}
				if mode == "closed stdout" {
					// 次の出力は、読み取り側を閉じた後にFIFOで許可する。
					if err := reader.Close(); err != nil {
						t.Fatal(err)
					}
					if _, err := io.WriteString(gate, "continue\n"); err != nil {
						t.Fatal(err)
					}
				} else if _, err := reader.(*os.File).Write([]byte{3}); err != nil {
					t.Fatal(err)
				}
				runErr = cmd.Wait()
				if mode == "Ctrl+C" {
					_, _ = io.Copy(&diagnostics, reader)
				}
			default:
				cmd.Stdout = &output
				runErr = cmd.Run()
			}
			code := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) {
					t.Fatal(runErr)
				}
				status := exitErr.Sys().(syscall.WaitStatus)
				if status.Signaled() {
					t.Fatalf("CLI terminated by %s; diagnostics=%q", status.Signal(), diagnostics.String())
				}
				code = exitErr.ExitCode()
			}
			paths := imageFiles(t, dir, "png")
			if ctx.Err() != nil || code != wantCode || len(paths) != wantPages {
				t.Fatalf("context=%v code=%d images=%v diagnostics=%q", ctx.Err(), code, paths, diagnostics.String())
			}
			for _, path := range paths {
				readImage(t, path)
				if mode == "closed stdout" && !strings.Contains(path, "-partial_") {
					t.Fatalf("partial image not marked: %s", path)
				}
			}
			switch mode {
			case "normal":
				if strings.TrimSpace(output.String()) != "ready" {
					t.Fatalf("output=%q", output.String())
				}
			case "child SIGPIPE":
				if strings.Contains(output.String(), "survived") || !strings.Contains(diagnostics.String(), "終了コードを取得できません") {
					t.Fatalf("child SIGPIPE changed: output=%q diagnostics=%q", output.String(), diagnostics.String())
				}
			case "closed stdout":
				if !strings.Contains(diagnostics.String(), "PTY実行・記録失敗") || !strings.Contains(diagnostics.String(), "broken pipe") || !strings.Contains(diagnostics.String(), "Partial capture: 2 PNG pages in ") {
					t.Fatalf("write failure or save result not reported: %q", diagnostics.String())
				}
			case "Ctrl+C":
				if !strings.Contains(diagnostics.String(), "中断されました") {
					t.Fatalf("interruption not reported: %q", diagnostics.String())
				}
			}
		})
	}
}

func TestCLIUnixPageConfirmation(t *testing.T) {
	for _, tc := range pagePromptCases {
		t.Run(tc.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "existing.png"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCLIPagePromptProcess$", "--", "pagetty-page-prompt", tc.mode, dir)
			master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			out := &consoleResponder{input: master, responses: tc.responses("\x04")}
			done := make(chan error, 1)
			go func() { _, err := io.Copy(out, master); done <- err }()
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, syscall.EIO) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			tc.check(t, dir, out)
		})
	}
}
