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
	"sync/atomic"
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
	Columns         int
	Rows            int
	Input           *os.File
	Output          io.Writer
	SaveOnInterrupt bool
	OnRecordLimit   func(reason string, running bool) // 書き込みを待たずに通知を受け取る。
}

// Result は記録エラー時にも取得済みのDocumentを保持する。
// 起動失敗はStarted=false、終了状態が得られなければExitCode=-1となる。
// 対象の非0終了だけではRunのエラーにしない。
// Interruptedは対象の割り込み終了、Canceledは記録処理自体の中断を示す。
// Canceledの場合、または割り込み終了時の保存が無効な場合はDocumentを破棄する。
type Result struct {
	Document display.Document
	// CommandEndedAt は終了待ちを終え、残りの出力を回収する前の時刻。
	CommandEndedAt time.Time
	Started        bool
	ExitCode       int
	Interrupted    bool
	Canceled       bool
	Limit          string
}

// Run はcommand[0]を残りの引数で直接実行する。同じ端末での同時呼び出しはしない。
// コンテキストの中断・入出力失敗はエラーで返す。返却後のDocumentは変更しない。
// サイズは実行中固定とし、親端末のリサイズには追従しない。
func Run(ctx context.Context, command []string, options Options) (result Result, resultErr error) {
	result.ExitCode = -1
	if len(command) == 0 || command[0] == "" {
		return result, errors.New("実行コマンドが指定されていません")
	}
	if err := ValidateSize(options.Columns, options.Rows); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	ctx, cancelCapture := context.WithCancel(ctx)
	defer cancelCapture()
	runCtx, stopCommand := context.WithCancel(ctx)
	defer stopCommand()
	outputCtx, stopOutput := context.WithCancel(ctx)
	defer stopOutput()

	p, reader, err := openPTY(options.Columns, options.Rows)
	if err != nil {
		return result, fmt.Errorf("PTYを作成できません: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, closePTY(p)) }()
	if reader == nil {
		reader = p
	} else {
		defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	}

	restoreInput := func() error { return nil }
	if options.Input != nil && term.IsTerminal(int(options.Input.Fd())) {
		fd := int(options.Input.Fd())
		state, err := term.MakeRaw(fd)
		if err != nil {
			return result, fmt.Errorf("入力端末を切り替えられません: %w", err)
		}
		restoreInput = func() error { return term.Restore(fd, state) }
	}
	defer func() { resultErr = errors.Join(resultErr, restoreInput()) }()
	restoreOutput, err := prepareOutput(options.Output)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, restoreOutput()) }()

	var input *inputReader
	closeInput := func() error { return nil }
	if options.Input != nil {
		input, err = openInput(options.Input, false)
		if err != nil {
			return result, err
		}
		closeInput = input.close
	}
	defer func() { resultErr = errors.Join(resultErr, closeInput()) }()

	cmd := newCommand(runCtx, command)
	cmd.Env = os.Environ()
	if err := startCommand(p, cmd); err != nil {
		return result, fmt.Errorf("コマンドを起動できません: %w", err)
	}
	result.Started = true
	if err := afterStart(p); err != nil {
		stopCommand()
		_ = cmd.Process.Kill()
		_ = xpty.WaitProcess(runCtx, cmd)
		return result, err
	}

	record := newRecording(options.Columns, options.Rows)
	defer func() { resultErr = errors.Join(resultErr, record.screen.terminal.Close()) }()
	f, hasFD := options.Output.(interface{ Fd() uintptr })
	terminalOutput := hasFD && term.IsTerminal(int(f.Fd()))
	terminalInput := options.Input != nil && term.IsTerminal(int(options.Input.Fd()))
	var ended atomic.Bool
	if options.OnRecordLimit != nil {
		record.onLimit = func(reason string) { options.OnRecordLimit(reason, !ended.Load()) }
	}
	var reply func([]byte) error
	if !terminalInput || !terminalOutput {
		reply = func(data []byte) error {
			if ended.Load() {
				return nil
			}
			if terminalOutput {
				return errors.New("端末問い合わせに応答するには端末のInputが必要です")
			}
			if err := writeOutput(runCtx, p, data); err != nil {
				if ended.Load() && (terminalEOF(err) || errors.Is(err, os.ErrDeadlineExceeded)) {
					return nil
				}
				return fmt.Errorf("端末問い合わせへの応答に失敗しました: %w", err)
			}
			return nil
		}
	}
	outputDone := make(chan error, 1)
	go func(done chan<- error) {
		done <- copyOutput(outputCtx, reader, options.Output, record, reply, stopCommand)
	}(outputDone)
	var inputDone chan error
	if input != nil {
		inputDone = make(chan error, 1)
		var target io.Writer = p
		if terminalInput {
			target = terminalInputForwarder{p, &ended, cancelCapture}
		}
		go func(done chan<- error) {
			_, err := io.Copy(target, input.reader)
			if err != nil && !inputCanceled(err) && !(ended.Load() && (terminalEOF(err) || errors.Is(err, os.ErrDeadlineExceeded))) {
				stopCommand()
				done <- fmt.Errorf("入力転送に失敗しました: %w", err)
				return
			}
			done <- nil
		}(inputDone)
	}

	waitErr := xpty.WaitProcess(runCtx, cmd)
	ended.Store(true)
	result.CommandEndedAt = time.Now()
	if input != nil {
		resultErr = errors.Join(resultErr, restoreInput())
		restoreInput = func() error { return nil }
		input.cancel()
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		result.Interrupted = wasInterrupted(cmd.ProcessState)
	}
	discard := result.Interrupted && !options.SaveOnInterrupt
	if discard {
		stopOutput()
	}
	resultErr = errors.Join(resultErr, stopPTYInput(p))
	if result.Interrupted {
		resultErr = errors.Join(resultErr, stopDescendants(cmd.Process.Pid))
	}
	if discard {
		resultErr = errors.Join(resultErr, stopPTYOutput(p, reader))
	}

	// WindowsのConPTYはクローズ時にも最後の画面を出力する。
	finished := make(chan error, 1)
	go func(done chan<- error) { done <- afterWait(p) }(finished)
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	deadline, canceled := timeout.C, ctx.Done()
	if discard {
		deadline = nil
	}
	stopped := result.Interrupted
	for outputDone != nil || finished != nil || inputDone != nil {
		select {
		case err := <-outputDone:
			resultErr = errors.Join(resultErr, err)
			outputDone = nil
		case err := <-finished:
			resultErr = errors.Join(resultErr, err)
			finished = nil
		case err := <-inputDone:
			resultErr = errors.Join(resultErr, err, closeInput())
			closeInput = func() error { return nil }
			inputDone = nil
		case <-deadline:
			resultErr = errors.Join(resultErr, errors.New("終了後のPTY出力を回収できませんでした（部分記録）"))
			stopOutput()
			resultErr = errors.Join(resultErr, stopPTYOutput(p, reader))
			deadline = nil
		case <-canceled:
			if !stopped {
				stopped = true
				resultErr = errors.Join(resultErr, stopDescendants(cmd.Process.Pid))
			}
			if !discard && (outputDone != nil || finished != nil) {
				resultErr = errors.Join(resultErr, stopPTYOutput(p, reader))
			}
			canceled, deadline = nil, nil
		}
	}
	resultErr = errors.Join(resultErr, waitFailure(waitErr))
	if ctx.Err() != nil {
		if !stopped {
			resultErr = errors.Join(resultErr, stopDescendants(cmd.Process.Pid))
		}
		result.Canceled = true
		return result, errors.Join(resultErr, ctx.Err())
	}
	if discard {
		return result, resultErr
	}
	result.Document, err = record.document(ctx)
	result.Limit = record.limited
	if ctx.Err() != nil {
		result.Canceled = true
		result.Document = display.Document{}
	}
	return result, errors.Join(resultErr, err)
}

type terminalInputForwarder struct {
	target io.Writer
	ended  *atomic.Bool
	cancel context.CancelFunc
}

func (w terminalInputForwarder) Write(data []byte) (n int, err error) {
	if !w.ended.Load() {
		n, err = w.target.Write(data)
		if !w.ended.Load() || n == len(data) {
			return n, err
		}
	}
	// 端末モードを戻す前に読み取ったCtrl+Cで、対象に渡らなかった分を処理する。
	if bytes.IndexByte(data[n:], 3) >= 0 {
		w.cancel()
	}
	if err != nil {
		return n, err
	}
	return len(data), nil
}

func waitFailure(err error) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result error
		for _, part := range joined.Unwrap() {
			result = errors.Join(result, waitFailure(part))
		}
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func copyOutput(ctx context.Context, src io.Reader, dst io.Writer, record *recording, reply func([]byte) error, stopCommand context.CancelFunc) error {
	var resultErr error
	forward, parse := true, true
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		n, err := src.Read(buf)
		if n > 0 && ctx.Err() == nil {
			if parse {
				if parseErr := record.consume(ctx, buf[:n], reply); parseErr != nil {
					if !stoppedOutput(ctx, parseErr) {
						resultErr = errors.Join(resultErr, parseErr)
						stopCommand()
					}
					parse = false
				}
			}
			if forward {
				if writeErr := writeOutput(ctx, dst, buf[:n]); writeErr != nil {
					if !stoppedOutput(ctx, writeErr) {
						resultErr = errors.Join(resultErr, fmt.Errorf("端末への出力に失敗しました: %w", writeErr))
						stopCommand()
					}
					forward = false
				}
			}
		}
		if err != nil {
			if !terminalEOF(err) && !(ctx.Err() != nil && errors.Is(err, os.ErrDeadlineExceeded)) {
				resultErr = errors.Join(resultErr, err)
				stopCommand()
			}
			break
		}
	}
	return resultErr
}

// WriteError は転送先で発生した障害、または転送の待機切れを表す。
type WriteError struct{ Err error }

func (e *WriteError) Error() string { return e.Err.Error() }
func (e *WriteError) Unwrap() error { return e.Err }

func stoppedOutput(ctx context.Context, err error) bool {
	var failure *WriteError
	return ctx.Err() != nil && errors.Is(err, ctx.Err()) && !errors.As(err, &failure)
}

var errWriteTimeout = errors.New("書き込みが停止しました（部分記録）")

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
		if err != nil {
			err = &WriteError{Err: err}
		}
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var waitErr error
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		waitErr = ctx.Err()
		if !time.Now().Before(deadline) {
			waitErr = &WriteError{Err: errWriteTimeout}
		}
	case <-timer.C:
		waitErr = &WriteError{Err: errWriteTimeout}
	}
	// 競合時は、すでに判明している実際の書き込み結果を優先する。
	select {
	case err := <-done:
		return err
	default:
		return waitErr
	}
}

// ReadTerminalInput は端末モードを変えずに確認用の入力を読み取る。
func ReadTerminalInput(ctx context.Context, file *os.File, read func(io.Reader) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := openInput(file, true)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		input.cancel()
		close(done)
	})
	defer func() {
		if !stop() {
			<-done
		}
		err = errors.Join(err, input.close())
	}()
	err = read(input.reader)
	if inputCanceled(err) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type inputReader struct {
	reader io.Reader
	cancel func()
	close  func() error
}
