package capture

import (
	"bufio"
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
	for _, mode := range []string{"nonzero", "missing", "cancel", "output failure"} {
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

func TestRunDescendantHoldingPTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := Run(ctx, childCommand("descendant"), Options{Columns: 40, Rows: 3, Output: io.Discard})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !result.Started || result.ExitCode != 0 ||
		!strings.Contains(strings.Join(rowTexts(result.Document), ""), "parent") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
