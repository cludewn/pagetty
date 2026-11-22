package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
	"tuios.dev/vt"
)

type progressPTYWriter struct {
	*os.File
	master *os.File
	screen *vt.Emulator
	mu     sync.Mutex
}

// PTYを通過した出力を、次のリサイズ操作より先に端末へ反映する。
func (w *progressPTYWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.File.Write(data)
	if err != nil {
		return n, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(w.master, buf); err != nil {
		return n, err
	}
	_, err = w.screen.Write(buf)
	return n, err
}

func (w *progressPTYWriter) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var lines []string
	var line strings.Builder
	for y := range w.screen.Height() {
		for x := range w.screen.Width() {
			if cell := w.screen.CellAt(x, y); cell != nil && cell.Content != "" {
				line.WriteString(cell.Content)
			} else {
				line.WriteByte(' ')
			}
		}
		if wrapped, _ := w.screen.RowSoftWrapped(y); !wrapped {
			if text := strings.TrimRight(line.String(), " "); text != "" {
				lines = append(lines, text)
			}
			line.Reset()
		}
	}
	return lines
}

func TestRenderProgressResizedPTY(t *testing.T) {
	const saving = "pagetty: Saving 7 pages..."
	const saved = "pagetty: Saved 7 PNG pages in 1s"
	for _, tc := range []struct {
		name        string
		start       uint16
		resizes     []uint16
		stopOnly    bool
		wantHistory bool
		next        string
	}{
		{name: "unchanged", start: 80, next: saved},
		{name: "shrink", start: 80, resizes: []uint16{24}, wantHistory: true, next: saved},
		{name: "unknown", start: 80, resizes: []uint16{0}, wantHistory: true, next: saved},
		{name: "resume", start: 80, resizes: []uint16{0, 80}, wantHistory: true, next: saved},
		{name: "widen", start: 24, resizes: []uint16{80}, next: saved},
		{name: "shrink before stop", start: 80, resizes: []uint16{24}, stopOnly: true, wantHistory: true, next: saved},
		{name: "unknown before stop", start: 80, resizes: []uint16{0}, stopOnly: true, wantHistory: true, next: saved},
		{name: "failed", start: 80, resizes: []uint16{0}, stopOnly: true, wantHistory: true, next: "pagetty: render failed"},
		{name: "interrupted partial capture", start: 80, resizes: []uint16{24}, wantHistory: true, next: "pagetty: Partial capture: 7 PNG pages in 1s"},
		{name: "shrink to too narrow", start: 80, resizes: []uint16{12}, wantHistory: true, next: saved},
		{name: "too narrow", start: 12, next: saved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rows = 24
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			// 書いたバイト数をそのままPTYから読み取れるようにする。
			if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
				t.Fatal(err)
			}
			if err := pty.Setsize(slave, &pty.Winsize{Cols: tc.start, Rows: rows}); err != nil {
				t.Fatal(err)
			}
			emu := vt.NewEmulator(int(tc.start), rows)
			defer emu.Close()
			w := &progressPTYWriter{File: slave, master: master, screen: emu}

			func() {
				width := renderTerminalWidth(w)
				if width == nil || width() != int(tc.start) {
					t.Fatal("could not get the stderr PTY width")
				}
				update, stop := startRenderProgress(w, width, time.Now())
				defer stop()
				update(0, 7)
				for i, cols := range tc.resizes {
					w.mu.Lock()
					err := pty.Setsize(slave, &pty.Winsize{Cols: cols, Rows: rows})
					if cols != 0 {
						emu.Resize(int(cols), rows)
					}
					w.mu.Unlock()
					if err != nil || width() != int(cols) {
						t.Fatalf("resize to %d: %v", cols, err)
					}
					if tc.stopOnly {
						continue
					}
					update(i+1, 7)
					if cols != 0 {
						want := renderProgressText(i+1, 7, 0, int(cols))
						if want != "" && !slices.Contains(w.lines(), want) {
							t.Fatalf("width %d: want new line %q, screen %q", cols, want, w.lines())
						}
					}
				}
			}()

			want := []string{tc.next}
			if tc.name != "failed" {
				want = append([]string{saving}, want...)
			}
			for _, line := range want {
				if _, err := fmt.Fprintf(w, "%s\r\n", line); err != nil {
					t.Fatal(err)
				}
			}
			got := w.lines()
			if tc.wantHistory {
				want = append([]string{"pagetty: Rendering 0/7 pages (0s)"}, want...)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("screen got %q, want %q", got, want)
			}
			t.Logf("screen %q", got)
		})
	}
}
