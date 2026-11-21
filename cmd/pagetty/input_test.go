package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCLIStdinModes(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pipe", "empty pipe", "file", "null", "no input"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			marker, images := filepath.Join(dir, "started"), filepath.Join(dir, "images")
			args := cliArgs([]string{"--output-dir", images}, "mark", marker)
			cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestCLIEntry$", "--", "pagetty-cli-entry"}, args...)...)
			switch mode {
			case "pipe":
				cmd.Stdin = strings.NewReader("hello\n")
			case "empty pipe":
				cmd.Stdin = strings.NewReader("")
			case "file", "null":
				path := os.DevNull
				if mode == "file" {
					path = filepath.Join(dir, "input.txt")
					if err := os.WriteFile(path, []byte("hello\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				input, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				cmd.Stdin = input
			}
			output, runErr := cmd.CombinedOutput()
			if ctx.Err() != nil || cmd.ProcessState == nil {
				t.Fatalf("context=%v error=%v output=%s", ctx.Err(), runErr, output)
			}
			if mode == "pipe" || mode == "empty pipe" || mode == "file" {
				if cmd.ProcessState.ExitCode() != 2 || !bytes.Contains(output, []byte("非対応")) || !bytes.Contains(output, []byte("子シェル")) {
					t.Fatalf("exit=%d error=%v output=%s", cmd.ProcessState.ExitCode(), runErr, output)
				}
				for _, path := range []string{marker, images} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("command started or directory created: %s (%v)", path, err)
					}
				}
			} else if runErr != nil || len(imageFiles(t, images, "png")) != 1 {
				t.Fatalf("no-input execution failed: error=%v output=%s", runErr, output)
			} else if data, err := os.ReadFile(marker); err != nil || string(data) != "started" {
				t.Fatalf("command not started: %q %v", data, err)
			}
		})
	}
}

func TestCLIShellFileInput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"png", "svg"} {
		for _, spaces := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/spaces=%t", format, spaces), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "work dir")
				relative, inputName := "a.out", "input.txt"
				if runtime.GOOS == "windows" {
					relative = "a.exe"
				}
				if spaces {
					relative = filepath.Join("program dir", relative)
					inputName = "input data.txt"
				}
				path := filepath.Join(dir, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, program, 0755); err != nil {
					t.Fatal(err)
				}
				input := "alpha\n日本語\nlast"
				if err := os.WriteFile(filepath.Join(dir, inputName), []byte(input), 0600); err != nil {
					t.Fatal(err)
				}
				helperArgs := "-test.run=^TestCLIChild$ -- pagetty-cli-child file-input"
				command := []string{"sh", "-c", "exec './" + filepath.ToSlash(relative) + "' " + helperArgs + " < '" + inputName + "'"}
				if runtime.GOOS == "windows" {
					command = []string{"cmd.exe", "/d", "/c", ".\\" + relative + " " + helperArgs + " < " + inputName}
					if spaces {
						command = []string{"cmd.exe", "/d", "/c", "call", ".\\" + relative, "-test.run=^TestCLIChild$", "--", "pagetty-cli-child", "file-input", "<", inputName}
					}
				}
				args := append([]string{"--format", format, "--max-lines", "1", "--output-dir", "images", "--"}, command...)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestCLIEntry$", "--", "pagetty-cli-entry"}, args...)...)
				cmd.Dir = dir
				var output, diagnostics bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &diagnostics
				err := cmd.Run()
				paths := imageFiles(t, filepath.Join(dir, "images"), format)
				if err != nil || ctx.Err() != nil || !strings.Contains(output.String(), fmt.Sprintf("INPUT:%q", input)) || !strings.Contains(output.String(), "tty:true:true") || len(paths) != 2 {
					t.Fatalf("error=%v context=%v images=%v output=%q diagnostics=%q", err, ctx.Err(), paths, output.String(), diagnostics.String())
				}
				for _, path := range paths {
					readImage(t, path)
				}
			})
		}
	}
}
