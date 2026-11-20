package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestCLIUnixInteractionAndCtrlC(t *testing.T) {
	for _, mode := range []string{"interactive", "ctrlc", "size"} {
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
			}
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var diagnostics bytes.Buffer
			code := runCLI(ctx, cliArgs([]string{"--output-dir", dir}, childMode), slave, out, &diagnostics)
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
			if mode == "size" && !strings.Contains(out.String(), "size:100:12:<nil>") {
				t.Fatalf("host terminal rows not used: %q", out.String())
			}
			paths := imageFiles(t, dir, "png")
			if len(paths) != 1 || (mode == "ctrlc" && !strings.Contains(paths[0], "-partial_001")) {
				t.Fatalf("paths=%v diagnostics=%q", paths, diagnostics.String())
			}
			readImage(t, paths[0])
		})
	}
}

func TestCLIUnixSignalExit(t *testing.T) {
	dir := t.TempDir()
	code, _, diagnostics := runTestCLI(t, []string{"capture", "--output-dir", dir, "--", "/bin/sh", "-c", "printf before; kill -KILL $$"})
	if code != 1 || !strings.Contains(diagnostics, "終了コードを取得できません") || len(imageFiles(t, dir, "png")) != 1 {
		t.Fatalf("code=%d diagnostics=%q", code, diagnostics)
	}
}
