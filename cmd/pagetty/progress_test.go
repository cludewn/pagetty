package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

type progressWrites chan string

func (w progressWrites) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestRenderProgressTicksWithoutPageCompletion(t *testing.T) {
	for _, tt := range []struct {
		name    string
		columns int
		want    string
	}{
		{"terminal", 80, "Rendering 0/7 pages"},
		{"narrow", 24, "pagetty: 0/7"},
		{"redirected", -1, "Rendering 0/7 pages"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := make(progressWrites, 16)
			var width func() int
			if tt.columns > 0 {
				width = func() int { return tt.columns }
			}
			update, stop := startRenderProgress(lines, width, time.Now())
			defer stop()
			update(0, 7)

			timer := time.NewTimer(4 * time.Second)
			defer timer.Stop()
			for {
				select {
				case line := <-lines:
					if width != nil {
						if !strings.HasPrefix(line, "\r") || strings.Contains(line, "\n") {
							t.Fatalf("terminal progress should update one line: %q", line)
						}
					} else if strings.ContainsAny(line, "\r\x1b") || !strings.HasSuffix(line, "\n") {
						t.Fatalf("redirected progress should use plain lines: %q", line)
					}
					if strings.Contains(line, tt.want) && !strings.Contains(line, "(0s)") {
						return
					}
				case <-timer.C:
					t.Fatal("elapsed time did not update while the first page was still rendering")
				}
			}
		})
	}
}

func TestRenderProgressTextFitsBeforeLastColumn(t *testing.T) {
	const full = "pagetty: Rendering 3/7 pages (36s)"
	const short = "pagetty: 3/7 (36s)"
	for _, tt := range []struct {
		columns, done, pages int
		seconds              int64
		want                 string
	}{
		{80, 3, 7, 36, full},
		{24, 3, 7, 36, short},
		{len(full), 3, 7, 36, short},
		{len(short), 3, 7, 36, ""},
		{12, 3, 7, 36, ""},
		{0, 3, 7, 36, ""},
		{-1, 3, 7, 36, full},
		{24, 99, 999, 9999, "pagetty: 99/999 (9999s)"},
		{24, 999, 9999, 99999, ""},
	} {
		if got := renderProgressText(tt.done, tt.pages, tt.seconds, tt.columns); got != tt.want {
			t.Errorf("width %d, %d/%d at %ds: got %q, want %q", tt.columns, tt.done, tt.pages, tt.seconds, got, tt.want)
		}
	}
}

func TestRenderProgressClearsPreviousLine(t *testing.T) {
	for _, columns := range []int{0, 12, 24, 80} {
		var log bytes.Buffer
		update, stop := startRenderProgress(&log, func() int { return columns }, time.Now().Add(-9999*time.Second))
		update(99, 999)
		update(999, 9999)
		stop()
		if columns <= 12 {
			if log.Len() != 0 {
				t.Errorf("width %d: suppressed progress wrote %q", columns, log.String())
			}
			continue
		}

		// CR ごとに先頭に戻り、終端時に前の表示が残らないことを確認する。
		line := make([]byte, columns-1)
		for i := range line {
			line[i] = ' '
		}
		for _, fragment := range strings.Split(log.String(), "\r") {
			if len(fragment) >= columns || strings.ContainsAny(fragment, "\n\x1b") {
				t.Fatalf("width %d: progress would wrap: %q", columns, fragment)
			}
			copy(line, fragment)
		}
		if !strings.Contains(log.String(), "99/999") || strings.TrimSpace(string(line)) != "" {
			t.Errorf("width %d: previous progress not cleared: %q", columns, log.String())
		}
	}
}
