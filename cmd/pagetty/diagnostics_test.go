package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cludewn/pagetty/internal/capture"
)

func TestCLICursorUpdateAtOnePageLimit(t *testing.T) {
	var reference []byte
	for _, mode := range []string{"updated-only", "cursor-update"} {
		dir := t.TempDir()
		options := []string{"--format", "svg", "--max-lines", "1", "--page-limit", "1", "--output-dir", dir}
		code, _, diagnostics := runTestCLI(t, cliArgs(options, mode))
		paths := imageFiles(t, dir, "svg")
		if code != 0 || len(paths) != 1 || strings.Contains(paths[0], "-partial_") || strings.Contains(diagnostics, "記録を打ち切りました") {
			t.Fatalf("%s: code=%d paths=%v diagnostics=%q", mode, code, paths, diagnostics)
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		if reference != nil && !bytes.Equal(reference, data) {
			t.Fatal("updated screen differs from rendering the final text")
		}
		reference = data
	}
}

type blockedWriter struct {
	trigger                    string
	entered, release, returned chan struct{}
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(w.trigger)) {
		close(w.entered)
		<-w.release
		close(w.returned)
	}
	return len(p), nil
}

func blockOnWrite(t *testing.T, trigger string) *blockedWriter {
	t.Helper()
	w := &blockedWriter{trigger, make(chan struct{}), make(chan struct{}), make(chan struct{})}
	t.Cleanup(func() {
		close(w.release)
		select {
		case <-w.entered:
			select {
			case <-w.returned:
			case <-time.After(time.Second):
				t.Error("blocked Write did not return after release")
			}
		default:
		}
	})
	return w
}

type outputMarker struct {
	bytes.Buffer
	tail chan struct{}
}

func (w *outputMarker) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if w.tail != nil && strings.Contains(w.String(), "OUTPUT-TAIL") {
		close(w.tail)
		w.tail = nil
	}
	return n, err
}

func TestCLIBlockedDiagnosticsDoesNotBlockCapture(t *testing.T) {
	for _, phase := range []struct {
		trigger, mode string
		wantCode      int
	}{
		{"記録を打ち切りました", "notice-tail", 1},
		{"Rendering", "text", 0},
		{"pagetty: Output:", "text", 0},
	} {
		t.Run(phase.mode+"/"+phase.trigger, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
			defer cancel()
			dir := t.TempDir()
			options := []string{"--format", "svg", "--columns", "160", "--max-lines", "1", "--page-limit", "1", "--output-dir", dir}
			stderr := blockOnWrite(t, phase.trigger)
			tail := make(chan struct{})
			stdout := &outputMarker{tail: tail}
			done := make(chan int, 1)
			go func() { done <- runCLI(ctx, cliArgs(options, phase.mode), nil, stdout, stderr) }()
			select {
			case <-stderr.entered:
			case <-ctx.Done():
				t.Fatal("test never reached the blocked diagnostic")
			}
			if phase.mode == "notice-tail" {
				select {
				case <-tail:
				case <-time.After(3 * time.Second):
					t.Fatal("stdout tail did not arrive while the limit notice was blocked")
				}
			}
			select {
			case code := <-done:
				if code != phase.wantCode || len(imageFiles(t, dir, "svg")) != 1 {
					t.Fatalf("code=%d path=%v", code, imageFiles(t, dir, "svg"))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("CLI did not finish while stderr was still blocked")
			}
		})
	}
}

func TestCLIOutputTimeoutKeepsDiagnostic(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	dir := t.TempDir()
	stdout := blockOnWrite(t, "ready")
	var stderr bytes.Buffer
	code := runCLI(ctx, cliArgs([]string{"--format", "svg", "--output-dir", dir}, "block"), nil, stdout, &stderr)
	paths := imageFiles(t, dir, "svg")
	if code != 1 || len(paths) != 1 || !strings.Contains(paths[0], "-partial_") ||
		!strings.Contains(stderr.String(), "端末への出力に失敗しました: 書き込みが停止しました") {
		t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, stderr.String())
	}
}

func TestWithoutCancellationPreservesOutputFailure(t *testing.T) {
	failure := &capture.WriteError{Err: context.DeadlineExceeded}
	actual := fmt.Errorf("stderr detail: %w", failure)
	got := withoutCancellation(errors.Join(fmt.Errorf("context only: %w", context.Canceled), actual, io.ErrShortWrite))
	if !errors.Is(got, failure) || !errors.Is(got, io.ErrShortWrite) || strings.Contains(got.Error(), "context only") {
		t.Fatalf("diagnostics=%v", got)
	}
}

func TestCLICaptureLimitNoticeOnce(t *testing.T) {
	for _, mode := range []string{"notice-tail", "count"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			opts := []string{"--format", "svg", "--columns", "160", "--max-lines", "1", "--page-limit", "1", "--output-dir", dir}
			code, _, diag := runTestCLI(t, cliArgs(opts, mode, "820"))
			paths := imageFiles(t, dir, "svg")
			if code != 1 || len(paths) != 1 || !strings.Contains(paths[0], "-partial_") ||
				strings.Count(diag, "記録を打ち切りました") != 1 || strings.Contains(diag, "記録上限に到達しました") ||
				!strings.Contains(diag, "対象の終了コード 0") || !strings.Contains(diag, "Partial capture:") {
				t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diag)
			}
			running := strings.Contains(diag, "対象プログラムの実行と入出力は継続します")
			if running != (mode == "notice-tail") {
				t.Fatalf("running notification=%t diagnostics=%q", running, diag)
			}
			if !strings.Contains(diag, "記録の列数×保持行数") ||
				strings.Index(diag, "記録を打ち切りました") > strings.Index(diag, "Partial capture:") {
				t.Fatalf("diagnostics=%q", diag)
			}
		})
	}
}
