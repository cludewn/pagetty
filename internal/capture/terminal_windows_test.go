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

	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func windowsTestCommand(mode string) []string {
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return []string{executable, "-test.run=^TestWindowsConsoleChild$", "--", "pagetty-console", mode}
}

func TestWindowsConsoleChild(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "pagetty-console" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "inner" {
		input := bufio.NewReader(os.Stdin)
		fmt.Print("first>")
		a, _ := input.ReadString('\n')
		fmt.Printf("got:%s\n", strings.TrimSpace(a))
		handle := windows.Handle(os.Stdin.Fd())
		var old uint32
		if windows.GetConsoleMode(handle, &old) != nil || windows.SetConsoleMode(handle, old&^windows.ENABLE_ECHO_INPUT) != nil {
			os.Exit(10)
		}
		fmt.Print("hidden>")
		secret, _ := input.ReadString('\n')
		if windows.SetConsoleMode(handle, old) != nil {
			os.Exit(11)
		}
		fmt.Printf("\nlength:%d\nthird>", len(strings.TrimSpace(secret)))
		b, _ := input.ReadString('\n')
		fmt.Printf("got:%s\n", strings.TrimSpace(b))
		os.Exit(0)
	}
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		os.Exit(12)
	}
	codePage, _ := windows.GetConsoleCP()
	var outputMode uint32
	if windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &outputMode) != nil {
		os.Exit(14)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := windowsTestCommand("inner")
	var output io.Writer = os.Stdout
	var stalled *stalledOutput
	switch mode {
	case "ctrlc":
		command = childCommand("block")
	case "nonzero":
		command = childCommand("nonzero")
	case "missing":
		command = []string{"pagetty-nonexistent-command.exe"}
	case "wrapped output":
		output = struct{ *os.File }{os.Stdout}
	case "stalled output":
		stalled = newStalledOutput("ready")
		command, output = childCommand("block"), stalled
		go func() {
			select {
			case <-stalled.entered:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	result, runErr := Run(ctx, command, Options{Columns: 40, Rows: 4, Input: os.Stdin, Output: output})
	after, stateErr := term.GetState(int(os.Stdin.Fd()))
	afterCP, cpErr := windows.GetConsoleCP()
	var afterOutputMode uint32
	outputErr := windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &afterOutputMode)
	restored := stateErr == nil && cpErr == nil && outputErr == nil && reflect.DeepEqual(before, after) &&
		codePage == afterCP && outputMode == afterOutputMode
	if stalled != nil {
		close(stalled.release)
		<-stalled.finished
		restored = restored && !stalled.closed.Load()
	}
	fmt.Printf("\nRESULT:%t:%d:%t:restored=%t:error=%t\n", result.Started, result.ExitCode, result.Interrupted, restored, runErr != nil)
	fmt.Printf("RECORD:%s\n", strings.Join(rowTexts(result.Document), "|"))
	os.Exit(0)
}

func TestWindowsConsoleInputAndRestore(t *testing.T) {
	for _, mode := range []string{"interactive", "wrapped output", "ctrlc", "nonzero", "missing", "stalled output"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// 外側のConPTYで、テスト対象に実際のWindowsコンソール入力を与える。
			p, reader, err := openPTY(200, 20)
			if err != nil {
				t.Fatal(err)
			}
			defer closePTY(p)
			defer reader.Close()
			cmd := newCommand(ctx, windowsTestCommand(mode))
			if err := startCommand(p, cmd); err != nil {
				t.Fatal(err)
			}
			out := newObservedOutput()
			readDone := make(chan error, 1)
			go func() {
				_, err := io.Copy(out, reader)
				readDone <- err
			}()
			switch mode {
			case "interactive", "wrapped output":
				for _, step := range []struct{ prompt, input string }{
					{"first>", "日本語\r"}, {"hidden>", "hidden-token\r"}, {"third>", "omega\r"},
				} {
					out.wait(t, ctx, step.prompt)
					if _, err := io.WriteString(p, step.input); err != nil {
						t.Fatal(err)
					}
				}
			case "ctrlc":
				out.wait(t, ctx, "ready")
				_, _ = p.Write([]byte{3})
			}
			if err := xpty.WaitProcess(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			if err := closePTY(p); err != nil {
				t.Fatal(err)
			}
			if err := <-readDone; err != nil && !terminalEOF(err) {
				t.Fatal(err)
			}
			s := newScreen(200, 20)
			_, _ = s.Write([]byte(out.text()))
			doc, err := s.document()
			_ = s.terminal.Close()
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(rowTexts(doc), "\n")
			if !strings.Contains(text, "restored=true") {
				t.Fatalf("console not restored: %q", text)
			}
			switch mode {
			case "interactive", "wrapped output":
				if !strings.Contains(text, "RESULT:true:0:false:restored=true:error=false") ||
					!strings.Contains(text, "RECORD:first>日本語|got:日本語|hidden>||length:12|third>omega|got:omega") ||
					strings.Contains(text, "hidden-token") {
					t.Fatalf("interactive result: %q", text)
				}
			case "ctrlc":
				if !strings.Contains(text, ":true:restored=true:error=false") || strings.Contains(text, "RECORD:ready") {
					t.Fatalf("Ctrl+C result: %q", text)
				}
			case "nonzero":
				if !strings.Contains(text, "RESULT:true:7:false:restored=true:error=false") {
					t.Fatalf("nonzero result: %q", text)
				}
			case "missing":
				if !strings.Contains(text, "RESULT:false:-1:false:restored=true:error=true") {
					t.Fatalf("start failure result: %q", text)
				}
			case "stalled output":
				if !strings.Contains(text, ":true:restored=true:error=true") || strings.Contains(text, "RECORD:ready") {
					t.Fatalf("stalled output result: %q", text)
				}
			}
		})
	}
}

func TestWindowsPipeStopUnblocksIO(t *testing.T) {
	for _, writing := range []bool{false, true} {
		t.Run(fmt.Sprint(writing), func(t *testing.T) {
			server, client, err := consolePipe(writing)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(client)
			p := &overlappedPipe{handle: server}
			defer p.Close()
			done := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				if writing {
					_, err := p.Write(make([]byte, 256*1024))
					done <- err
				} else {
					_, err := p.Read(make([]byte, 64))
					done <- err
				}
			}()
			<-started
			select {
			case err := <-done:
				t.Fatalf("I/O did not block: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if err := p.stop(false); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("stop has error %v", err)
				}
			case <-time.After(time.Second):
				_ = windows.DisconnectNamedPipe(server)
				t.Fatal("I/O did not stop")
			}
		})
	}
}

func TestWindowsPipeCloseIsIdempotent(t *testing.T) {
	server, client, err := consolePipe(false)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(client)
	p := &overlappedPipe{handle: server}
	for i := range 2 {
		if err := p.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
	if err := p.stop(true); err != nil {
		t.Fatalf("stop after Close: %v", err)
	}
	if _, err := p.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Read after Close: %v", err)
	}
}
