package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cludewn/pagetty/internal/capture"
	"github.com/cludewn/pagetty/internal/render"
)

func confirmPages(ctx context.Context, input *os.File, diagnostics io.Writer, total, setting, available int, hardLimit render.Limit, partial bool) (int, error) {
	var message strings.Builder
	fmt.Fprintf(&message, "pagetty: 画像の生成枚数（%d枚）が設定上限（%d枚）を超えています。\n", total, setting)
	if partial {
		fmt.Fprintln(&message, "記録に欠落があるため、保存する画像は部分記録です。")
	}
	if hardLimit != "" {
		fmt.Fprintf(&message, "安全上の上限（%s）により、保存可能な範囲は先頭の%d枚です。\n", hardLimit, available)
	}

	fmt.Fprintln(&message, "※ 生成時の容量制限により、保存枚数が少なくなる場合があります。")
	fmt.Fprintln(&message)
	prompt := "選択 [l/N]: "
	switch {
	case hardLimit == "":
		if partial {
			fmt.Fprintf(&message, "  [a] 記録済みの全ページを保存（%d枚、部分記録）\n", total)
		} else {
			fmt.Fprintf(&message, "  [a] すべて保存（%d枚）\n", total)
		}
		prompt = "選択 [a/l/N]: "
	case available > setting:
		fmt.Fprintf(&message, "  [a] 保存可能な範囲を保存（%d枚、部分記録）\n", available)
		prompt = "選択 [a/l/N]: "
	}
	fmt.Fprintf(&message, "  [l] 上限まで保存（%d枚）\n  [n] 保存しない\n\n%s", min(setting, available), prompt)

	var count int
	err := capture.ReadTerminalInput(ctx, input, func(reader io.Reader) error {
		if _, err := io.WriteString(diagnostics, message.String()); err != nil {
			return err
		}
		buffer := bufio.NewReader(reader)
		tooLong := false
		for {
			line, err := buffer.ReadSlice('\n')
			switch {
			case errors.Is(err, bufio.ErrBufferFull):
				tooLong = true
				continue
			case errors.Is(err, io.EOF):
				return nil
			case err != nil:
				return err
			}
			if !tooLong {
				switch strings.ToLower(strings.TrimSpace(string(line))) {
				case "n", "", "\x1a":
					return nil
				case "a":
					if available > setting {
						count = total
						return nil
					}
				case "l":
					count = setting
					return nil
				}
			}
			tooLong = false
			if _, err := fmt.Fprintf(diagnostics, "pagetty: 選択が無効です。\n%s", prompt); err != nil {
				return err
			}
		}
	})
	return count, err
}
