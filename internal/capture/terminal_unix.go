//go:build !windows

package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/charmbracelet/x/termios"
	"github.com/charmbracelet/x/xpty"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
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
	if !ok || !status.Signaled() {
		return false
	}
	// SIGKILLだけでは中断要求か判別できない。コンテキストの中断はRun側で反映する。
	switch status.Signal() {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP:
		return true
	default:
		return false
	}
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

func finishInput(ctx context.Context, p xpty.Pty, input *os.File) error {
	if term.IsTerminal(int(input.Fd())) {
		return nil
	}
	var state *unix.Termios
	var stateErr error
	err := p.(*xpty.UnixPty).Control(func(fd uintptr) {
		state, stateErr = termios.GetTermios(int(fd))
	})
	if err := errors.Join(err, stateErr); err != nil {
		return fmt.Errorf("PTYのEOF設定を取得できません: %w", err)
	}
	if state.Lflag&unix.ICANON == 0 {
		return errors.New("入力EOFの通知はcanonical modeのPTYだけに対応しています")
	}
	eof := state.Cc[unix.VEOF]
	if eof == 0 || eof == 255 {
		return errors.New("PTYのEOF文字が無効または未対応です（VEOF=0/255）")
	}
	// 改行のない末尾を最初のVEOFで渡し、次のVEOFで長さ0のreadを発生させる。
	return writeOutput(ctx, p, []byte{eof, eof})
}

func prepareOutput(io.Writer) (func() error, error) {
	return func() error { return nil }, nil
}
