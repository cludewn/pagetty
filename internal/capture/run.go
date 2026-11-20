// Package capture は、対話型プログラムをPTYで実行し、解釈済みの表示を取得する。
package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/xpty"
	"github.com/cludewn/pagetty/internal/display"
	"golang.org/x/term"
)

// Options は記録時の固定サイズと入出力先を指定する。
// Inputがnilなら入力を転送しない。端末なら実行中だけrawモードにする。
// Windowsではコンソール入力のみ対応する。入力元のEOFをPTYのEOFへは変換しない。
// Outputがnilなら呼び出し元の標準出力を使う。入出力先の所有権は呼び出し元に残る。
// Writeが5秒を超えて停止した場合は転送を打ち切るが、進行中のWrite自体は終了できない。
// 返却後のOutput再利用・解放は、呼び出し元でそのWriteと同期する必要がある。
// OutputはFd() uintptrが実端末を示す場合だけホスト応答を使う。ラッパーも同じ条件とする。
// 非端末Writerでは仮想端末が応答する。
// 端末出力で端末のInputがない場合、問い合わせは応答を転送できないためエラーにする。
type Options struct {
	Columns int
	Rows    int
	Input   *os.File
	Output  io.Writer
}

// Result はエラー時にも取得済みのDocumentを保持する。
// 起動失敗はStarted=false、終了状態が得られなければExitCode=-1となる。
// 対象の非0終了だけではRunのエラーにしない。
// Interrupted は中断シグナルによる終了、または呼び出し元コンテキストの中断を示す。
type Result struct {
	Document    display.Document
	Started     bool
	ExitCode    int
	Interrupted bool
}

// Run はcommand[0]を残りの引数で直接実行する。同じ端末での同時呼び出しはしない。
// コンテキストの中断・入出力失敗はエラーで返す。返却後のDocumentは変更しない。
// サイズは実行中固定とし、親端末のリサイズには追従しない。
func Run(ctx context.Context, command []string, options Options) (result Result, resultErr error) {
	result.ExitCode = -1
	if len(command) == 0 || command[0] == "" {
		return result, errors.New("実行コマンドが指定されていません")
	}
	if options.Columns < 2 || options.Columns > 32767 || options.Rows < 1 || options.Rows > 32767 {
		return result, errors.New("端末サイズは列2〜32767、行1〜32767で指定してください")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	p, reader, err := openPTY(options.Columns, options.Rows)
	if err != nil {
		return result, fmt.Errorf("PTYを作成できません: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, closePTY(p))
	}()
	if reader != nil {
		defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	}
	if reader == nil {
		reader = p
	}

	if options.Input != nil && term.IsTerminal(int(options.Input.Fd())) {
		state, err := term.MakeRaw(int(options.Input.Fd()))
		if err != nil {
			return result, fmt.Errorf("入力端末を切り替えられません: %w", err)
		}
		defer func() {
			resultErr = errors.Join(resultErr, term.Restore(int(options.Input.Fd()), state))
		}()
	}
	restoreOutput, err := prepareOutput(options.Output)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, restoreOutput()) }()

	var input *inputReader
	if options.Input != nil {
		input, err = openInput(options.Input)
		if err != nil {
			return result, err
		}
		defer func() { resultErr = errors.Join(resultErr, input.close()) }()
	}

	cmd := newCommand(runCtx, command)
	cmd.Env = os.Environ()
	if err := startCommand(p, cmd); err != nil {
		return result, fmt.Errorf("コマンドを起動できません: %w", err)
	}
	result.Started = true
	if err := afterStart(p); err != nil {
		cancel()
		_ = cmd.Process.Kill()
		_ = xpty.WaitProcess(runCtx, cmd)
		return result, err
	}

	s := newScreen(options.Columns, options.Rows)
	defer func() { resultErr = errors.Join(resultErr, s.terminal.Close()) }()
	f, hasFD := options.Output.(interface{ Fd() uintptr })
	terminalOutput := hasFD && term.IsTerminal(int(f.Fd()))
	terminalInput := options.Input != nil && term.IsTerminal(int(options.Input.Fd()))
	var reply func([]byte) error
	if !terminalInput || !terminalOutput {
		reply = func(data []byte) error {
			if terminalOutput {
				return errors.New("端末問い合わせに応答するには端末のInputが必要です")
			}
			return writeOutput(runCtx, p, data)
		}
	}
	outputDone := make(chan error, 1)
	go func() { outputDone <- copyOutput(runCtx, reader, options.Output, s, reply, cancel) }()
	inputDone := make(chan error, 1)
	var inputStopped atomic.Bool
	if input != nil {
		go func() {
			_, err := io.Copy(p, input.reader)
			if inputStopped.Load() || inputCanceled(err) {
				err = nil
			} else if err != nil {
				cancel()
			}
			inputDone <- err
		}()
	}

	waitErr := xpty.WaitProcess(runCtx, cmd)
	if input != nil {
		inputStopped.Store(true)
		input.cancel()
	}
	// ConPTYは終了時にも出力を送る。読み取りを継続したまま閉じる。
	finishDone := make(chan error, 1)
	go func() { finishDone <- afterWait(p) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err = <-outputDone:
	case <-timer.C:
		resultErr = errors.New("終了後のPTY出力を回収できませんでした（部分記録）")
		cancel()
		_ = closePTY(p)
		err = <-outputDone
	}
	resultErr = errors.Join(resultErr, err, <-finishDone)
	resultErr = errors.Join(resultErr, closePTY(p))
	if input != nil {
		if err := <-inputDone; err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("入力転送に失敗しました: %w", err))
		}
	}
	result.Document, err = s.document()
	resultErr = errors.Join(resultErr, err)
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		result.Interrupted = wasInterrupted(cmd.ProcessState)
	}
	if ctx.Err() != nil {
		result.Interrupted = true
		resultErr = errors.Join(resultErr, ctx.Err())
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, context.Canceled) {
		resultErr = errors.Join(resultErr, waitErr)
	}
	return result, resultErr
}

func copyOutput(ctx context.Context, src io.Reader, dst io.Writer, s *screen, reply func([]byte) error, cancel context.CancelFunc) error {
	var resultErr error
	forward := true
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, parseErr := s.Write(buf[:n]); parseErr != nil {
				resultErr = errors.Join(resultErr, parseErr)
				cancel()
			}
			responses := s.terminal.TakeResponses()
			if reply != nil && len(responses) > 0 {
				if replyErr := reply(responses); replyErr != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("端末問い合わせへの応答に失敗しました: %w", replyErr))
					cancel()
				}
			}
			if forward {
				writeErr := writeOutput(ctx, dst, buf[:n])
				if writeErr != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("端末への出力に失敗しました: %w", writeErr))
					forward = false
					cancel()
				}
			}
		}
		if err != nil {
			if !terminalEOF(err) {
				resultErr = errors.Join(resultErr, err)
				cancel()
			}
			return resultErr
		}
	}
}

func writeOutput(ctx context.Context, dst io.Writer, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 停止したWriteが返却後も参照できるよう、記録側の再利用バッファと分離する。
	data := bytes.Clone(p)
	done := make(chan error, 1)
	go func() {
		n, err := dst.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("書き込みが停止しました（部分記録）: %w", context.DeadlineExceeded)
	}
}

type inputReader struct {
	reader io.Reader
	cancel func()
	close  func() error
}
