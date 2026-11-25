package capture

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cludewn/pagetty/internal/display"
)

const (
	MaxColumns     = 3273
	maxScreenCells = 65536
	maxRecordRows  = 4096
	maxRecordCells = 131072
	maxRecordBytes = 2 * 1024 * 1024
)

func ValidateSize(columns, rows int) error {
	if columns < 2 || columns > MaxColumns || rows < 1 || rows > 32767 || rows > maxScreenCells/columns {
		return fmt.Errorf("端末は2〜%d列、1〜32767行、合計%dセル以下で指定してください", MaxColumns, maxScreenCells)
	}
	return nil
}

const historyOverwrite = "履歴の上書きで記録の先頭が欠落"

type recording struct {
	screen             *screen
	maxRows, remaining int
	frozen             display.Document
	limited            string
	rowCause           string
	overwritten        bool
	prefix             *historyPrefix
	onLimit            func(string)
}

type historyPrefix struct {
	doc display.Document
	err error
}

func newRecording(columns, rows int) *recording {
	capacity := min(maxRecordRows, maxRecordCells/columns)
	cause := "最大記録表示行数"
	if capacity < maxRecordRows {
		cause = "記録の列数×保持行数"
	}
	s := newScreen(columns, rows)
	r := &recording{screen: s, maxRows: capacity, remaining: maxRecordBytes, rowCause: cause}
	sb := s.terminal.Scrollback()
	sb.SetMaxLines(capacity)
	markers := s.terminal.SemanticMarkers()
	sb.SetOnTrim(func(n int) {
		markers.AdjustForScrollbackTrim(n)
		// vt v0.3.3は上書きに加え、履歴を空にする消去でも通知する。
		if sb.Len() != 0 {
			r.overwritten = true
		} else if !r.overwritten {
			r.prefix = nil
		}
	})
	return r
}

func (r *recording) consume(ctx context.Context, data []byte, reply func([]byte) error) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.limited != "" {
			if reply == nil {
				return nil
			}
			return r.feed(data, reply)
		}
		if r.remaining == 0 {
			if err := r.freeze(ctx, "受信データ量"); err != nil {
				return err
			}
			continue
		}
		sb := r.screen.terminal.Scrollback()
		room := sb.MaxLines() - sb.Len()
		if room == 0 {
			// ちょうど満杯になった履歴だけを退避し、次の上書きまで記録を続ける。
			if r.prefix == nil {
				doc, _, err := r.screen.documentUntil(ctx, r.maxRows)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.prefix = &historyPrefix{doc, err}
			}
			room = sb.MaxLines()
		}
		// 通常は1バイトにつき最大1行進む。制御命令の一括スクロールは
		// 上書きの通知で検出する。
		n := min(len(data), r.remaining, room)
		err := r.feed(data[:n], reply)
		r.remaining -= n
		data = data[n:]
		// 画面内の行は上書きや消去が可能なので、実行中は履歴だけで判定する。
		if r.overwritten || sb.Len() > r.maxRows {
			reason := r.rowCause
			if r.overwritten && r.prefix == nil {
				reason = historyOverwrite
			}
			err = errors.Join(err, r.freeze(ctx, reason))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *recording) feed(data []byte, reply func([]byte) error) error {
	if n, err := r.screen.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if response := r.screen.terminal.TakeResponses(); reply != nil && len(response) > 0 {
		return reply(response)
	}
	return nil
}

func (r *recording) freeze(ctx context.Context, reason string) error {
	var err error
	if r.overwritten && r.prefix != nil {
		r.frozen, err = r.prefix.doc, r.prefix.err
	} else {
		r.frozen, _, err = r.screen.documentUntil(ctx, r.maxRows)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.prefix = nil
	r.markLimited(reason)
	r.screen.terminal.DiscardScrollback()
	r.screen.terminal.ReleaseScrollbackCache()
	return err
}

func (r *recording) document(ctx context.Context) (display.Document, error) {
	if err := ctx.Err(); err != nil {
		return display.Document{}, err
	}
	if r.limited != "" {
		return r.frozen, nil
	}
	doc, truncated, err := r.screen.documentUntil(ctx, r.maxRows)
	if truncated {
		r.markLimited(r.rowCause)
	}
	return doc, err
}

func (r *recording) markLimited(reason string) {
	r.limited = reason
	if r.onLimit != nil {
		r.onLimit(reason)
	}
}
