package capture

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func openPTY(columns, rows int) (xpty.Pty, io.ReadCloser, error) {
	p, err := xpty.NewPty(columns, rows)
	if err != nil {
		return nil, nil, err
	}
	// ConPTY.Closeが元の出力ハンドルを閉じても、残った出力をEOFまで回収する。
	var handle windows.Handle
	process := windows.CurrentProcess()
	err = windows.DuplicateHandle(process, windows.Handle(p.(*xpty.ConPty).OutPipeReadFd()),
		process, &handle, 0, false, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		_ = p.Close()
		return nil, nil, err
	}
	return p, os.NewFile(uintptr(handle), "conpty-output"), nil
}

func newCommand(_ context.Context, command []string) *exec.Cmd {
	// ConPTYの起動はexec.Cmdの監視処理を通らない。中断はWaitProcessに任せる。
	return exec.Command(command[0], command[1:]...)
}

func startCommand(p xpty.Pty, cmd *exec.Cmd) error {
	if cmd.Err != nil {
		return cmd.Err
	}
	pid, handle, err := p.(*xpty.ConPty).Spawn(cmd.Path, cmd.Args, &syscall.ProcAttr{Env: cmd.Env})
	if err != nil {
		return err
	}
	// Spawnが返すハンドルと、os.FindProcessが取得するハンドルは別に解放する。
	defer windows.CloseHandle(windows.Handle(handle))
	cmd.Process, err = os.FindProcess(pid)
	if err != nil {
		_ = windows.TerminateProcess(windows.Handle(handle), 1)
	}
	return err
}

func afterStart(xpty.Pty) error  { return nil }
func afterWait(p xpty.Pty) error { return p.Close() }
func closePTY(p xpty.Pty) error  { return p.Close() }

func terminalEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, os.ErrClosed)
}

func wasInterrupted(state *os.ProcessState) bool {
	return uint32(state.ExitCode()) == uint32(windows.STATUS_CONTROL_C_EXIT)
}

type consoleReader struct {
	handle windows.Handle
	stop   windows.Handle
}

func openInput(f *os.File) (*inputReader, error) {
	if !term.IsTerminal(int(f.Fd())) {
		return nil, errors.New("Windowsの入力転送にはコンソール入力が必要です")
	}
	name, err := windows.UTF16PtrFromString("CONIN$")
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	codePage, err := windows.GetConsoleCP()
	if err == nil {
		// ReadFileで受け取るキー入力を、ConPTYが要求するUTF-8に揃える。
		err = windows.SetConsoleCP(65001)
	}
	if err != nil {
		_ = windows.CloseHandle(handle)
		_ = windows.CloseHandle(stop)
		return nil, err
	}
	r := &consoleReader{handle: handle, stop: stop}
	return &inputReader{
		reader: r,
		cancel: func() { _ = windows.SetEvent(stop) },
		close: func() error {
			return errors.Join(windows.CloseHandle(handle), windows.CloseHandle(stop), windows.SetConsoleCP(codePage))
		},
	}, nil
}

func (r *consoleReader) Read(p []byte) (int, error) {
	if ready, err := windows.WaitForSingleObject(r.stop, 0); err != nil {
		return 0, err
	} else if ready == windows.WAIT_OBJECT_0 {
		return 0, context.Canceled
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlap := windows.Overlapped{HEvent: event}
	var n uint32
	err = windows.ReadFile(r.handle, p, &n, &overlap)
	if err == nil {
		return int(n), nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return int(n), err
	}
	ready, waitErr := windows.WaitForMultipleObjects([]windows.Handle{r.stop, event}, false, windows.INFINITE)
	if waitErr != nil || ready != windows.WAIT_OBJECT_0+1 {
		_ = windows.CancelIoEx(r.handle, &overlap)
		_ = windows.GetOverlappedResult(r.handle, &overlap, &n, true)
		if waitErr != nil {
			return 0, waitErr
		}
		if ready != windows.WAIT_OBJECT_0 {
			return 0, errors.New("コンソール入力の待機に失敗しました")
		}
		return 0, context.Canceled
	}
	err = windows.GetOverlappedResult(r.handle, &overlap, &n, false)
	return int(n), err
}

func inputCanceled(err error) bool { return errors.Is(err, context.Canceled) }

func finishInput(context.Context, xpty.Pty, *os.File) error { return nil }

func prepareOutput(w io.Writer) (func() error, error) {
	f, ok := w.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() error { return nil }, nil
	}
	handle := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(handle, mode) }, nil
}
