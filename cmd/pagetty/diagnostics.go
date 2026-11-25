package main

import (
	"bytes"
	"io"
	"os"
	"sync"
	"time"
)

const diagnosticWait = time.Second

type diagnosticMessage struct {
	data   []byte
	result chan diagnosticResult
}

type diagnosticResult struct {
	written int
	err     error
}

// 通知・進捗・終了診断は一つの書き手で順に送る。任意のWriterが
// 停滞した場合、CLIは書き手の完了を待たず、後続の診断も止める。
type diagnosticWriter struct {
	queue   chan diagnosticMessage
	stopped chan struct{}
	stop    sync.Once
}

func newDiagnosticWriter(dst io.Writer) *diagnosticWriter {
	w := &diagnosticWriter{queue: make(chan diagnosticMessage, 1), stopped: make(chan struct{})}
	go func() {
		for {
			select {
			case <-w.stopped:
				return
			case msg := <-w.queue:
				select {
				case <-w.stopped:
					return
				default:
				}
				n, err := dst.Write(msg.data)
				if err == nil && n != len(msg.data) {
					err = io.ErrShortWrite
				}
				msg.result <- diagnosticResult{n, err}
				if err != nil {
					w.close()
					return
				}
			}
		}
	}()
	return w
}

func (w *diagnosticWriter) close() {
	w.stop.Do(func() { close(w.stopped) })
}

// 記録打ち切り通知は一実行につき一度だけで、PTY側から完了を待たない。
func (w *diagnosticWriter) notice(text string) <-chan diagnosticResult {
	select {
	case <-w.stopped:
		return nil
	default:
	}
	result := make(chan diagnosticResult, 1)
	select {
	case w.queue <- diagnosticMessage{data: []byte(text), result: result}:
		return result
	default:
		return nil
	}
}

func (w *diagnosticWriter) Write(p []byte) (int, error) {
	select {
	case <-w.stopped:
		return 0, io.ErrClosedPipe
	default:
	}
	timer := time.NewTimer(diagnosticWait)
	defer timer.Stop()
	msg := diagnosticMessage{data: bytes.Clone(p), result: make(chan diagnosticResult, 1)}
	select {
	case w.queue <- msg:
	case <-w.stopped:
		return 0, io.ErrClosedPipe
	case <-timer.C:
		w.close()
		return 0, os.ErrDeadlineExceeded
	}
	select {
	case result := <-msg.result:
		return result.written, result.err
	case <-w.stopped:
		select {
		case result := <-msg.result:
			return result.written, result.err
		default:
			return 0, io.ErrClosedPipe
		}
	case <-timer.C:
		w.close()
		return 0, os.ErrDeadlineExceeded
	}
}
