package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/term"
)

func childCommand(mode string, args ...string) []string {
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return append([]string{executable, "-test.run=^TestCaptureChild$", "--", "pagetty-child", mode}, args...)
}

func TestCaptureChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "pagetty-child" || i+1 >= len(os.Args) {
			continue
		}
		switch os.Args[i+1] {
		case "args":
			b, _ := json.Marshal(os.Args[i+2:])
			fmt.Print(string(b))
		case "nonzero":
			fmt.Print("before exit")
			os.Exit(7)
		case "lines":
			for n := range 10050 {
				fmt.Printf("%05d\n", n)
			}
		case "block":
			fmt.Print("ready")
			time.Sleep(time.Hour)
		case "flood":
			fmt.Print("ready" + strings.Repeat("z", 200000))
		case "tail", "tail-limit":
			count := 120000
			if os.Args[i+1] == "tail-limit" {
				count = 200000
			}
			fmt.Print(strings.Repeat("x", count) + "END")
		case "terminal":
			fmt.Printf("tty:%t:%t:%t", term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())), term.IsTerminal(int(os.Stderr.Fd())))
			fmt.Fprint(os.Stderr, ":stderr")
		case "descendant":
			fmt.Print("parent")
			ready, notify, err := os.Pipe()
			if err != nil {
				os.Exit(13)
			}
			command := childCommand("hold")
			cmd := exec.Command(command[0], command[1:]...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			cmd.ExtraFiles = []*os.File{notify}
			if cmd.Start() != nil {
				os.Exit(13)
			}
			_ = notify.Close()
			var b [1]byte
			_, _ = ready.Read(b[:])
		case "hold":
			signal.Ignore(syscall.SIGHUP)
			notify := os.NewFile(3, "ready")
			_, _ = notify.Write([]byte{1})
			_ = notify.Close()
			time.Sleep(6 * time.Second)
		}
		os.Exit(0)
	}
}

func TestRunExitAndArguments(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
		code    int
		text    string
	}{
		{"arguments", childCommand("args", "with space", "", "; echo bad", "$(echo bad)", "日本語", `a"b`, `end\`), 0,
			`["with space","","; echo bad","$(echo bad)","日本語","a\"b","end\\"]`},
		{"nonzero", childCommand("nonzero"), 7, "before exit"},
		{"terminal streams", childCommand("terminal"), 0, "tty:true:true:true:stderr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var out bytes.Buffer
			before := time.Now()
			result, err := Run(ctx, tc.command, Options{Columns: 256, Rows: 3, Output: &out})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Started || result.ExitCode != tc.code || result.Interrupted {
				t.Fatalf("result=%+v", result)
			}
			if result.CommandEndedAt.Before(before) || result.CommandEndedAt.After(time.Now()) {
				t.Fatalf("command end time outside Run: %v", result.CommandEndedAt)
			}
			if got := strings.Join(rowTexts(result.Document), ""); got != tc.text {
				t.Fatalf("display=%q, want %q", got, tc.text)
			}
			live := newScreen(256, 3)
			_, _ = live.Write(out.Bytes())
			liveDoc, liveErr := live.document()
			_ = live.terminal.Close()
			if liveErr != nil || strings.Join(rowTexts(liveDoc), "") != tc.text {
				t.Fatalf("live display=%q err=%v", rowTexts(liveDoc), liveErr)
			}
		})
	}
}

func TestRunStartFailure(t *testing.T) {
	result, err := Run(context.Background(), []string{"/pagetty-nonexistent-command"}, Options{Columns: 20, Rows: 3, Output: io.Discard})
	if err == nil || result.Started || result.ExitCode != -1 || !result.CommandEndedAt.IsZero() || len(result.Document.Rows) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunLongOutputAndTail(t *testing.T) {
	for _, mode := range []string{"lines", "tail", "tail-limit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var out bytes.Buffer
			result, err := Run(ctx, childCommand(mode), Options{Columns: 80, Rows: 3, Output: &out})
			if err != nil {
				t.Fatal(err)
			}
			checkDocument(t, result.Document)
			rows := rowTexts(result.Document)
			if mode == "lines" {
				if len(rows) != maxRecordCells/80 || result.Limit == "" || !strings.Contains(out.String(), "10049") {
					t.Fatalf("rows=%d limit=%q live tail=%t", len(rows), result.Limit, strings.Contains(out.String(), "10049"))
				}
				for i, row := range rows {
					if row != fmt.Sprintf("%05d", i) {
						t.Fatalf("row %d=%q", i, row)
					}
				}
			} else {
				want := strings.Repeat("x", 120000) + "END"
				if mode == "tail-limit" {
					want = strings.Repeat("x", maxRecordCells/80*80)
					if result.Limit == "" {
						t.Fatal("record limit was not reported")
					}
				} else if result.Limit != "" {
					t.Fatal("tail was unexpectedly limited")
				}
				if got := strings.Join(rows, ""); got != want {
					t.Fatalf("captured tail lost: length=%d, want %d", len(got), len(want))
				}
				if !strings.Contains(out.String(), "END") {
					t.Fatal("live output tail lost")
				}
			}
		})
	}
}

type observedOutput struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	notify chan struct{}
}

func newObservedOutput() *observedOutput {
	return &observedOutput{notify: make(chan struct{}, 1)}
}

func (o *observedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buf.Write(p)
	o.mu.Unlock()
	select {
	case o.notify <- struct{}{}:
	default:
	}
	return n, err
}

func (o *observedOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *observedOutput) wait(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	for !strings.Contains(o.text(), text) {
		select {
		case <-o.notify:
		case <-ctx.Done():
			t.Fatalf("waiting for %q: %v; output=%q", text, ctx.Err(), o.text())
		}
	}
}

func TestRunCancelDiscardsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := newObservedOutput()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := Run(ctx, childCommand("block"), Options{Columns: 20, Rows: 3, Output: out})
		done <- outcome{result, err}
	}()
	out.wait(t, ctx, "ready")
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || !got.result.Started || !got.result.Canceled || got.result.Interrupted ||
		len(got.result.Document.Rows) != 0 {
		t.Fatalf("result=%+v err=%v", got.result, got.err)
	}
}

type inputWriteFunc func([]byte) (int, error)

func (f inputWriteFunc) Write(data []byte) (int, error) { return f(data) }

func TestTerminalInputForwarderAtCommandExit(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		ended, cancel, forwarded bool
		data                     string
		written                  int
		writeErr                 error
	}{
		{"late control input", true, true, false, "\x03", 0, nil},
		{"late ordinary input", true, false, false, "a", 0, nil},
		{"interrupt forwarded", false, false, true, "\x03", 1, nil},
		{"undelivered interrupt", false, true, true, "a\x03", 1, os.ErrClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ended atomic.Bool
			ended.Store(tc.ended)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			forwarded := false
			target := inputWriteFunc(func(data []byte) (int, error) {
				forwarded = true
				ended.Store(true)
				return tc.written, tc.writeErr
			})
			w := terminalInputForwarder{target, &ended, cancel}
			_, err := w.Write([]byte(tc.data))
			if forwarded != tc.forwarded || (ctx.Err() != nil) != tc.cancel || !errors.Is(err, tc.writeErr) {
				t.Fatalf("forwarded=%t ctx=%v err=%v", forwarded, ctx.Err(), err)
			}
		})
	}
}

type brokenOutput struct{ seen string }

func (w *brokenOutput) Write(p []byte) (int, error) {
	w.seen += string(p)
	if strings.Contains(w.seen, "ready") {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestRunOutputFailureKeepsRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := Run(ctx, childCommand("block"), Options{Columns: 20, Rows: 3, Output: &brokenOutput{}})
	if !errors.Is(err, io.ErrClosedPipe) || !strings.Contains(strings.Join(rowTexts(result.Document), ""), "ready") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type stalledOutput struct {
	marker   string
	seen     string
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
	once     sync.Once
	closed   atomic.Bool
	mutated  atomic.Bool
}

func newStalledOutput(marker string) *stalledOutput {
	return &stalledOutput{marker: marker, entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
}

func (w *stalledOutput) Write(p []byte) (int, error) {
	w.seen += string(p)
	if strings.Contains(w.seen, w.marker) {
		w.once.Do(func() {
			expected := bytes.Clone(p)
			close(w.entered)
			<-w.release
			w.mutated.Store(!bytes.Equal(p, expected))
			close(w.finished)
		})
	}
	return len(p), nil
}

func (w *stalledOutput) Close() error { w.closed.Store(true); return nil }

func TestRunStalledOutput(t *testing.T) {
	for _, mode := range []string{"exit", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command, marker := childCommand("block"), "ready"
			if mode == "exit" {
				command, marker = childCommand("nonzero"), "before exit"
			} else if mode == "timeout" {
				command = childCommand("flood")
			}
			writer := newStalledOutput(marker)
			type outcome struct {
				result Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := Run(ctx, command, Options{Columns: 40, Rows: 3, Output: writer})
				done <- outcome{result, err}
			}()
			t.Cleanup(func() {
				cancel()
				close(writer.release)
				select {
				case <-writer.finished:
					if writer.mutated.Load() {
						t.Error("pending Write buffer changed after Run returned")
					}
				case <-time.After(2 * time.Second):
					t.Error("writer did not finish after release")
				}
			})
			select {
			case <-writer.entered:
			case <-ctx.Done():
				t.Fatal("writer did not start")
			}
			limit := 7 * time.Second
			if mode == "cancel" {
				cancel()
				limit = 2 * time.Second
			}
			select {
			case got := <-done:
				hasRecord := strings.Contains(strings.Join(rowTexts(got.result.Document), ""), marker)
				if got.err == nil || !got.result.Started || writer.closed.Load() || hasRecord == (mode == "cancel") {
					t.Fatalf("started=%t closed=%t display=%q err=%v", got.result.Started, writer.closed.Load(), rowTexts(got.result.Document), got.err)
				}
				if mode == "cancel" && (!errors.Is(got.err, context.Canceled) || !got.result.Canceled) {
					t.Fatalf("cancel result=%+v err=%v", got.result, got.err)
				}
			case <-time.After(limit):
				t.Fatal("Run blocked on caller-owned Output.Write")
			}
		})
	}
}
