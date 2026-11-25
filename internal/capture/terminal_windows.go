package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/conpty"
	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

type windowsPTY struct {
	*xpty.ConPty
	input *overlappedPipe
}

func (p *windowsPTY) Write(b []byte) (int, error) { return p.input.Write(b) }

type overlappedPipe struct {
	handle   windows.Handle
	ioMu, mu sync.Mutex
	stopped  bool
}

func (p *overlappedPipe) Read(data []byte) (int, error)  { return p.transfer(data, windows.ReadFile) }
func (p *overlappedPipe) Write(data []byte) (int, error) { return p.transfer(data, windows.WriteFile) }

func (p *overlappedPipe) transfer(data []byte, op func(windows.Handle, []byte, *uint32, *windows.Overlapped) error) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	p.ioMu.Lock()
	defer p.ioMu.Unlock()
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return 0, os.ErrClosed
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		p.mu.Unlock()
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlap := windows.Overlapped{HEvent: event}
	var pins runtime.Pinner
	pins.Pin(&overlap)
	pins.Pin(&data[0])
	defer pins.Unpin()
	var n uint32
	// I/Oを発行してからロックを離し、停止とのすれ違いを防ぐ。
	err = op(p.handle, data, &n, &overlap)
	p.mu.Unlock()
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		err = windows.GetOverlappedResult(p.handle, &overlap, &n, true)
	}
	if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		err = os.ErrClosed
	}
	return int(n), err
}

func (p *overlappedPipe) stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	err := windows.CancelIoEx(p.handle, nil)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}

func (p *overlappedPipe) Close() error {
	p.ioMu.Lock()
	defer p.ioMu.Unlock()
	return windows.CloseHandle(p.handle)
}

func openPTY(columns, rows int) (xpty.Pty, io.ReadCloser, error) {
	inHost, inConsole, err := consolePipe(true)
	if err != nil {
		return nil, nil, err
	}
	outHost, outConsole, err := consolePipe(false)
	if err != nil {
		return nil, nil, errors.Join(err, windows.CloseHandle(inHost), windows.CloseHandle(inConsole))
	}
	var in, out *overlappedPipe
	fail := func(err error) (xpty.Pty, io.ReadCloser, error) {
		if in != nil {
			err = errors.Join(err, in.Close())
		}
		if out != nil {
			err = errors.Join(err, out.Close())
		}
		for _, h := range []windows.Handle{inHost, inConsole, outHost, outConsole} {
			err = errors.Join(err, windows.CloseHandle(h))
		}
		return nil, nil, err
	}
	if in, err = duplicatePipe(inHost); err != nil {
		return fail(err)
	}
	if out, err = duplicatePipe(outHost); err != nil {
		return fail(err)
	}
	handle, err := conpty.NewWithPipes(uintptr(inConsole), uintptr(inHost), uintptr(outHost), uintptr(outConsole), columns, rows, 0)
	if err != nil {
		return fail(err)
	}
	return &windowsPTY{&xpty.ConPty{ConPty: handle}, in}, out, nil
}

func consolePipe(input bool) (host, console windows.Handle, err error) {
	var token [16]byte
	rand.Read(token[:])
	name, err := windows.UTF16PtrFromString(`\\.\pipe\pagetty-` + hex.EncodeToString(token[:]))
	if err != nil {
		return 0, 0, err
	}
	direction, access := uint32(windows.PIPE_ACCESS_INBOUND), uint32(windows.GENERIC_WRITE)
	if input {
		direction, access = windows.PIPE_ACCESS_OUTBOUND, windows.GENERIC_READ
	}
	// ConPTY側は同期I/O、ホスト側は中断可能な非同期I/Oにする。
	host, err = windows.CreateNamedPipe(name, direction|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 4096, 4096, 0, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("ConPTYパイプ作成: %w", err)
	}
	console, err = windows.CreateFile(name, access, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, 0, errors.Join(err, windows.CloseHandle(host))
	}
	return host, console, nil
}

func duplicatePipe(handle windows.Handle) (*overlappedPipe, error) {
	process := windows.CurrentProcess()
	var dup windows.Handle
	if err := windows.DuplicateHandle(process, handle, process, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	return &overlappedPipe{handle: dup}, nil
}

func newCommand(_ context.Context, command []string) *exec.Cmd {
	// ConPTYの起動はexec.Cmdの監視処理を通らない。中断はWaitProcessに任せる。
	return exec.Command(command[0], command[1:]...)
}

func startCommand(p xpty.Pty, cmd *exec.Cmd) error {
	if cmd.Err != nil {
		return cmd.Err
	}
	pid, handle, err := p.(*windowsPTY).Spawn(cmd.Path, cmd.Args, &syscall.ProcAttr{Env: cmd.Env})
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

func closePTY(p xpty.Pty) error {
	return errors.Join(p.Close(), p.(*windowsPTY).input.Close())
}

func stopPTYInput(p xpty.Pty) error {
	return p.(*windowsPTY).input.stop()
}

func stopPTYOutput(_ xpty.Pty, reader io.ReadCloser) error {
	r := reader.(*overlappedPipe)
	err := r.stop()
	// 古いWindowsでは排出不能な出力があるとClosePseudoConsoleも戻らない。
	disconnectErr := windows.DisconnectNamedPipe(r.handle)
	if errors.Is(disconnectErr, windows.ERROR_PIPE_NOT_CONNECTED) {
		disconnectErr = nil
	}
	return errors.Join(err, disconnectErr)
}

func stopDescendants(int) error { return nil } // ConPTYの終了で接続中のプロセスを閉じる。

func terminalEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) || errors.Is(err, os.ErrClosed)
}

func wasInterrupted(state *os.ProcessState) bool {
	return uint32(state.ExitCode()) == uint32(windows.STATUS_CONTROL_C_EXIT)
}

type consoleReader struct {
	handle       windows.Handle
	stop         windows.Handle
	confirmation bool
}

func openInput(f *os.File, confirmation bool) (*inputReader, error) {
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
	r := &consoleReader{handle: handle, stop: stop, confirmation: confirmation}
	return &inputReader{
		reader: r,
		cancel: func() { _ = windows.SetEvent(stop) },
		close: func() error {
			return errors.Join(windows.CloseHandle(handle), windows.CloseHandle(stop), windows.SetConsoleCP(codePage))
		},
	}, nil
}

func (r *consoleReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
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
	var pins runtime.Pinner
	pins.Pin(&overlap)
	pins.Pin(&p[0])
	defer pins.Unpin()
	var n uint32
	err = windows.ReadFile(r.handle, p, &n, &overlap)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return r.readResult(n, overlap.Internal, err)
	}
	ready, waitErr := windows.WaitForMultipleObjects([]windows.Handle{event, r.stop}, false, windows.INFINITE)
	if waitErr != nil || ready != windows.WAIT_OBJECT_0 {
		_ = windows.CancelIoEx(r.handle, &overlap)
		if windows.GetOverlappedResult(r.handle, &overlap, &n, true) == nil && n != 0 {
			return int(n), nil
		}
		if waitErr != nil {
			return 0, waitErr
		}
		if ready != windows.WAIT_OBJECT_0+1 {
			return 0, errors.New("コンソール入力の待機に失敗しました")
		}
		return 0, context.Canceled
	}
	err = windows.GetOverlappedResult(r.handle, &overlap, &n, false)
	return r.readResult(n, overlap.Internal, err)
}

func (r *consoleReader) readResult(n uint32, status uintptr, err error) (int, error) {
	if r.confirmation && n == 0 {
		// ReadFileはCtrl+ZとCtrl+Cの両方を0バイトで返すことがある。
		if status == uintptr(windows.STATUS_ALERTED) || errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
			return 0, context.Canceled
		}
		if err == nil {
			return 0, io.EOF
		}
	}
	return int(n), err
}

func inputCanceled(err error) bool { return errors.Is(err, context.Canceled) }

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
