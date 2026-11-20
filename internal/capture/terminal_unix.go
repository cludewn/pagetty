//go:build !windows

package capture

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/charmbracelet/x/xpty"
	"github.com/muesli/cancelreader"
)

func openPTY(columns, rows int) (xpty.Pty, io.ReadCloser, error) {
	p, err := xpty.NewPty(columns, rows)
	if err != nil {
		return nil, nil, err
	}
	// creack/ptyの初期化でblockingになるため、Closeで読み書きを解除できる状態に戻す。
	var modeErr error
	err = p.(*xpty.UnixPty).Control(func(fd uintptr) { modeErr = syscall.SetNonblock(int(fd), true) })
	if err = errors.Join(err, modeErr); err != nil {
		_ = closePTY(p)
		return nil, nil, err
	}
	return p, nil, nil
}

func newCommand(ctx context.Context, command []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}

func startCommand(p xpty.Pty, cmd *exec.Cmd) error { return p.Start(cmd) }

func afterStart(p xpty.Pty) error {
	// 親がslaveを保持すると、子の終了後もmasterにEOFが届かない。
	return p.(*xpty.UnixPty).Slave().Close()
}

func afterWait(xpty.Pty) error { return nil }

func closePTY(p xpty.Pty) error {
	u := p.(*xpty.UnixPty)
	return errors.Join(closeFile(u.Master()), closeFile(u.Slave()))
}

func closeFile(f *os.File) error {
	err := f.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func terminalEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

func wasInterrupted(state *os.ProcessState) bool {
	status, ok := state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

func openInput(f *os.File) (*inputReader, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		return &inputReader{reader: f, cancel: func() {}, close: func() error { return nil }}, nil
	}
	r, err := cancelreader.NewReader(f)
	if err != nil {
		return nil, err
	}
	return &inputReader{reader: r, cancel: func() { r.Cancel() }, close: r.Close}, nil
}

func inputCanceled(err error) bool { return errors.Is(err, cancelreader.ErrCanceled) }

func prepareOutput(io.Writer) (func() error, error) {
	return func() error { return nil }, nil
}
