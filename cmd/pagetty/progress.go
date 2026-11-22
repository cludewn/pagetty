package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

func isTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(f.Fd()))
}

func pageWord(count int) string {
	if count == 1 {
		return "page"
	}
	return "pages"
}

func startRenderProgress(w io.Writer, interactive bool, started time.Time) (update func(int, int), stop func()) {
	var mu sync.Mutex
	var done, total int
	var last string
	draw := func() {
		elapsed := time.Since(started) / time.Second
		line := fmt.Sprintf("pagetty: Rendering (%ds)", elapsed)
		if total > 0 {
			line = fmt.Sprintf("pagetty: Rendering %d/%d %s (%ds)", done, total, pageWord(total), elapsed)
		}
		if line == last {
			return
		}
		if interactive {
			fmt.Fprintf(w, "\r%s%s", line, strings.Repeat(" ", max(0, len(last)-len(line))))
		} else {
			fmt.Fprintln(w, line)
		}
		last = line
	}
	draw()
	update = func(completed, count int) {
		mu.Lock()
		defer mu.Unlock()
		done, total = completed, count
		draw()
	}
	cancel, finished := make(chan struct{}), make(chan struct{})
	ticker := time.NewTicker(time.Second)
	go func() {
		defer close(finished)
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				draw()
				mu.Unlock()
			case <-cancel:
				return
			}
		}
	}()
	return update, func() {
		ticker.Stop()
		close(cancel)
		<-finished
		if interactive {
			fmt.Fprintln(w)
		}
	}
}
