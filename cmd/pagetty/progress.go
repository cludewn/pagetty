package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// nil は非端末。端末の幅が取得できなければ 0 を返す。
func renderTerminalWidth(w io.Writer) func() int {
	f, ok := w.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	return func() int {
		width, _, err := term.GetSize(int(f.Fd()))
		if err != nil {
			return 0
		}
		return width
	}
}

func pageWord(count int) string {
	if count == 1 {
		return "page"
	}
	return "pages"
}

func renderProgressText(done, total int, seconds int64, columns int) string {
	line := fmt.Sprintf("pagetty: Rendering (%ds)", seconds)
	if total > 0 {
		line = fmt.Sprintf("pagetty: Rendering %d/%d %s (%ds)", done, total, pageWord(total), seconds)
	}
	// 最終列へ書くと自動折り返しが発生する端末がある。
	if columns == -1 || len(line) < columns {
		return line
	}
	line = fmt.Sprintf("pagetty: (%ds)", seconds)
	if total > 0 {
		line = fmt.Sprintf("pagetty: %d/%d (%ds)", done, total, seconds)
	}
	if len(line) < columns {
		return line
	}
	return ""
}

func startRenderProgress(w io.Writer, width func() int, started time.Time) (update func(int, int), stop func()) {
	var mu sync.Mutex
	var done, total int
	var last string
	var lastWidth int
	paint := func(line string, columns int) {
		// 縮小や幅不明でリフローした可能性がある行は、改行して閉じる。
		if width != nil && last != "" && columns < lastWidth {
			fmt.Fprint(w, "\r\n")
			last = ""
		}
		if line == last {
			return
		}
		if width == nil {
			fmt.Fprintln(w, line)
		} else {
			padding := max(0, len(last)-len(line))
			end := ""
			if line == "" {
				end = "\r"
			}
			fmt.Fprintf(w, "\r%s%s%s", line, strings.Repeat(" ", padding), end)
		}
		last, lastWidth = line, columns
	}
	draw := func() {
		columns := -1
		if width != nil {
			columns = max(0, width())
		}
		seconds := int64(time.Since(started) / time.Second)
		paint(renderProgressText(done, total, seconds, columns), columns)
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
		if width != nil {
			paint("", max(0, width()))
		}
	}
}
