package capture

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestUnixCaptureChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "pagetty-interactive" {
		return
	}
	input := bufio.NewReader(os.Stdin)
	fmt.Print("first>")
	a, _ := input.ReadString('\n')
	fmt.Printf("got:%s", a)
	state, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		os.Exit(10)
	}
	hidden := *state
	hidden.Lflag &^= unix.ECHO
	if unix.IoctlSetTermios(0, unix.TCSETS, &hidden) != nil {
		os.Exit(11)
	}
	fmt.Print("hidden>")
	secret, _ := input.ReadString('\n')
	if unix.IoctlSetTermios(0, unix.TCSETS, state) != nil {
		os.Exit(12)
	}
	fmt.Printf("\nlength:%d\nthird>", len(strings.TrimSpace(secret)))
	b, _ := input.ReadString('\n')
	fmt.Printf("got:%s", b)
	os.Exit(0)
}

func TestRunInteractiveEchoAndRestore(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := newObservedOutput()
	var result Result
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, runErr = Run(ctx, []string{os.Args[0], "-test.run=^TestUnixCaptureChild$", "--", "pagetty-interactive"},
			Options{Columns: 40, Rows: 4, Input: slave, Output: out})
	}()
	for _, step := range []struct{ prompt, input string }{
		{"first>", "alpha\r"}, {"hidden>", "hidden-token\r"}, {"third>", "omega\r"},
	} {
		out.wait(t, ctx, step.prompt)
		if _, err := io.WriteString(master, step.input); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	if runErr != nil || result.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", result.ExitCode, runErr)
	}
	text := strings.Join(rowTexts(result.Document), "\n")
	want := "first>alpha\ngot:alpha\nhidden>\nlength:12\nthird>omega\ngot:omega"
	if text != want {
		t.Fatalf("display=%q, want %q", text, want)
	}
	if strings.Contains(out.text(), "hidden-token") || strings.Contains(text, "hidden-token") {
		t.Fatal("hidden input was echoed or recorded")
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("parent terminal not restored: err=%v", err)
	}
}

func TestParentTerminalRestoreOnFailure(t *testing.T) {
	for _, mode := range []string{"nonzero", "missing", "cancel", "output failure", "stalled output"} {
		t.Run(mode, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			before, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := childCommand("nonzero")
			var output io.Writer = io.Discard
			out := newObservedOutput()
			switch mode {
			case "missing":
				command = []string{"/pagetty-nonexistent-command"}
			case "cancel":
				command, output = childCommand("block"), out
				go func() {
					select {
					case <-out.notify:
						cancel()
					case <-ctx.Done():
					}
				}()
			case "output failure":
				command, output = childCommand("block"), &brokenOutput{}
			case "stalled output":
				writer := newStalledOutput("ready")
				command, output = childCommand("block"), writer
				defer func() {
					close(writer.release)
					select {
					case <-writer.finished:
					case <-time.After(2 * time.Second):
						t.Error("writer did not finish after release")
					}
				}()
				go func() {
					select {
					case <-writer.entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			_, _ = Run(ctx, command, Options{Columns: 40, Rows: 3, Input: slave, Output: output})
			after, err := term.GetState(int(slave.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("parent terminal not restored: err=%v", err)
			}
		})
	}
}

func TestRunCtrlC(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := newObservedOutput()
	var result Result
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, runErr = Run(ctx, childCommand("block"), Options{Columns: 20, Rows: 3, Input: slave, Output: out})
	}()
	out.wait(t, ctx, "ready")
	_, _ = master.Write([]byte{3})
	<-done
	if runErr != nil || !result.Interrupted || !strings.Contains(strings.Join(rowTexts(result.Document), ""), "ready") {
		t.Fatalf("result=%+v err=%v", result, runErr)
	}
}

func TestRunSignalExit(t *testing.T) {
	for _, tc := range []struct {
		signal      string
		interrupted bool
	}{
		{"SEGV", false},
		{"ABRT", false},
		{"ILL", false},
		{"INT", true},
		{"TERM", true},
		{"HUP", true},
		{"KILL", false},
	} {
		t.Run(tc.signal, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := Run(ctx, []string{"/bin/sh", "-c", "printf before; kill -" + tc.signal + " $$"},
				Options{Columns: 20, Rows: 3, Output: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Started || result.ExitCode != -1 || result.Interrupted != tc.interrupted {
				t.Fatalf("started=%t exit=%d interrupted=%t, want started=true exit=-1 interrupted=%t",
					result.Started, result.ExitCode, result.Interrupted, tc.interrupted)
			}
			if got := rowTexts(result.Document); !reflect.DeepEqual(got, []string{"before"}) {
				t.Fatalf("display=%q, want [before]", got)
			}
		})
	}
}

func TestRunDescendantHoldingPTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := Run(ctx, childCommand("descendant"), Options{Columns: 40, Rows: 3, Output: io.Discard})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !result.Started || result.ExitCode != 0 ||
		!strings.Contains(strings.Join(rowTexts(result.Document), ""), "parent") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTerminalQueryChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "pagetty-query" {
		return
	}
	if _, err := term.MakeRaw(0); err != nil {
		os.Exit(10)
	}
	fmt.Print("before\x1b[6n")
	reader := bufio.NewReader(os.Stdin)
	reply, err := reader.ReadString('R')
	if err != nil {
		os.Exit(11)
	}
	fmt.Printf("reply:%q", reply)
	ready, pollErr := unix.Poll([]unix.PollFd{{Fd: 0, Events: unix.POLLIN}}, 50)
	if pollErr != nil || reader.Buffered() > 0 || ready > 0 {
		fmt.Print("duplicate response")
		os.Exit(12)
	}
	os.Exit(0)
}

func TestRunHeadlessQuery(t *testing.T) {
	for _, mode := range []string{"no input", "buffer output", "terminal input", "terminal input with bytes.Buffer", "terminal input with opaque writer", "pipe input", "terminal output without input"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			options := Options{Columns: 40, Rows: 3, Output: io.Discard}
			switch mode {
			case "buffer output":
				options.Output = newObservedOutput()
			case "terminal input", "terminal input with bytes.Buffer", "terminal input with opaque writer", "terminal output without input":
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer slave.Close()
				if mode != "terminal output without input" {
					options.Input = slave
					if mode == "terminal input with bytes.Buffer" {
						options.Output = new(bytes.Buffer)
					} else if mode == "terminal input with opaque writer" {
						options.Output = newObservedOutput()
					}
				} else {
					options.Output = slave
				}
			case "pipe input":
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				defer writer.Close()
				options.Input = reader
			}
			var inputState *term.State
			if options.Input != nil && term.IsTerminal(int(options.Input.Fd())) {
				var stateErr error
				inputState, stateErr = term.GetState(int(options.Input.Fd()))
				if stateErr != nil {
					t.Fatal(stateErr)
				}
			}
			command := []string{os.Args[0], "-test.run=^TestTerminalQueryChild$", "--", "pagetty-query"}
			result, err := Run(ctx, command, options)
			if mode == "terminal output without input" {
				if err == nil || !result.Started || !strings.Contains(strings.Join(rowTexts(result.Document), ""), "before") {
					t.Fatalf("missing input result: started=%t display=%q err=%v", result.Started, rowTexts(result.Document), err)
				}
				return
			}
			if inputState != nil {
				if state, stateErr := term.GetState(int(options.Input.Fd())); stateErr != nil || !reflect.DeepEqual(inputState, state) {
					t.Fatalf("parent terminal mode not restored: state=%+v err=%v", state, stateErr)
				}
			}
			if err != nil || !result.Started || result.ExitCode != 0 ||
				strings.Join(rowTexts(result.Document), "") != `beforereply:"\x1b[1;7R"` {
				t.Fatalf("exit=%d display=%q err=%v", result.ExitCode, rowTexts(result.Document), err)
			}
		})
	}
}

func TestRunExternalQueryResponder(t *testing.T) {
	for _, mode := range []string{"terminal file", "wrapped terminal"} {
		t.Run(mode, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out := newObservedOutput()
			readDone := make(chan struct{})
			go func() { _, _ = io.Copy(out, master); close(readDone) }()
			var output io.Writer = slave
			if mode == "wrapped terminal" {
				output = struct{ *os.File }{slave}
			}
			var result Result
			var runErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				command := []string{os.Args[0], "-test.run=^TestTerminalQueryChild$", "--", "pagetty-query"}
				result, runErr = Run(ctx, command, Options{Columns: 40, Rows: 3, Input: slave, Output: output})
			}()
			out.wait(t, ctx, "\x1b[6n")
			if _, err := io.WriteString(master, "\x1b[9;10R"); err != nil {
				t.Fatal(err)
			}
			<-done
			_ = slave.Close()
			<-readDone
			if runErr != nil || result.ExitCode != 0 ||
				strings.Join(rowTexts(result.Document), "") != `beforereply:"\x1b[9;10R"` {
				t.Fatalf("exit=%d display=%q err=%v", result.ExitCode, rowTexts(result.Document), runErr)
			}
		})
	}
}

func TestUnixInputEOFChild(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "pagetty-input-eof" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	state, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		os.Exit(10)
	}
	state.Lflag &^= unix.ECHO
	switch mode {
	case "custom":
		state.Cc[unix.VEOF] = 6
	case "disabled":
		state.Cc[unix.VEOF] = 0
	case "raw":
		state.Lflag &^= unix.ICANON
	}
	if unix.IoctlSetTermios(0, unix.TCSETS, state) != nil {
		os.Exit(11)
	}
	fmt.Print("ready>")
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(12)
	}
	fmt.Printf("DATA:%q", data)
	if mode == "nonzero" {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestRunInputEOF(t *testing.T) {
	for _, tc := range []struct {
		name, mode, input string
		code              int
		unsupported       bool
	}{
		{name: "empty"},
		{name: "newline", input: "hello\n"},
		{name: "no newline", input: "hello"},
		{name: "multiple lines", input: "first\n日本語\nlast"},
		{name: "long input", input: strings.Repeat("first\n", 200) + "last"},
		{name: "custom EOF", mode: "custom", input: "hello"},
		{name: "nonzero", mode: "nonzero", input: "hello\n", code: 7},
		{name: "raw mode", mode: "raw", unsupported: true},
		{name: "disabled EOF", mode: "disabled", unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out := newObservedOutput()
			var result Result
			var runErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				command := []string{os.Args[0], "-test.run=^TestUnixInputEOFChild$", "--", "pagetty-input-eof", tc.mode}
				result, runErr = Run(ctx, command, Options{Columns: 100, Rows: 4, Input: input, Output: out})
			}()
			out.wait(t, ctx, "ready>")
			if _, err := io.WriteString(writer, tc.input); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			<-done
			if ctx.Err() != nil || !result.Started || result.Interrupted {
				t.Fatalf("context=%v result=%+v error=%v", ctx.Err(), result, runErr)
			}
			text := strings.Join(rowTexts(result.Document), "")
			if tc.unsupported {
				if runErr == nil || !strings.Contains(runErr.Error(), "EOF") || !strings.Contains(text, "ready>") {
					t.Fatalf("unsupported mode: document=%q error=%v", text, runErr)
				}
				return
			}
			want := fmt.Sprintf("ready>DATA:%q", tc.input)
			if runErr != nil || result.ExitCode != tc.code || text != want {
				t.Fatalf("exit=%d document=%q want=%q error=%v", result.ExitCode, text, want, runErr)
			}
		})
	}
}
