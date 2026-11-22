package main

import (
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
		name string
		tty  bool
	}{{"terminal", true}, {"redirected", false}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := make(progressWrites, 16)
			update, stop := startRenderProgress(lines, tt.tty, time.Now())
			defer stop()
			update(0, 7)

			timer := time.NewTimer(4 * time.Second)
			defer timer.Stop()
			for {
				select {
				case line := <-lines:
					if tt.tty {
						if !strings.HasPrefix(line, "\r") || strings.Contains(line, "\n") {
							t.Fatalf("terminal progress should update one line: %q", line)
						}
					} else if strings.ContainsAny(line, "\r\x1b") || !strings.HasSuffix(line, "\n") {
						t.Fatalf("redirected progress should use plain lines: %q", line)
					}
					if strings.Contains(line, "0/7 pages") && !strings.Contains(line, "(0s)") {
						return
					}
				case <-timer.C:
					t.Fatal("elapsed time did not update while the first page was still rendering")
				}
			}
		})
	}
}
