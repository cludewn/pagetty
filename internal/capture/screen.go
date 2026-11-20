package capture

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/cludewn/pagetty/internal/display"
	"tuios.dev/vt"
)

type screen struct {
	terminal *vt.Emulator
}

func newScreen(columns, rows int) *screen {
	t := vt.NewEmulator(columns, rows)
	// リングの既定上限で過去行を捨てない。内部の添字加算が溢れない余地も残す。
	t.SetScrollbackMaxLines(math.MaxInt / 4)
	return &screen{terminal: t}
}

func (s *screen) Write(p []byte) (int, error) {
	return s.terminal.Write(p)
}

func (s *screen) document() (display.Document, error) {
	t := s.terminal
	doc := display.Document{Columns: t.Width()}
	var resultErr error
	appendRow := func(at func(int) *uv.Cell) {
		row, err := displayRow(t.Width(), at)
		doc.Rows = append(doc.Rows, row)
		resultErr = errors.Join(resultErr, err)
	}
	for y := 0; y < t.ScrollbackLen(); y++ {
		line := t.ScrollbackLine(y)
		appendRow(func(x int) *uv.Cell {
			if x >= len(line) {
				return nil
			}
			return &line[x]
		})
	}

	// 最後の改行で移動しただけの空行と、起動時の未使用領域は加えない。
	end := t.CursorPosition().Y
	if t.CursorPosition().X > 0 || t.CursorPendingWrap() {
		end++
	}
	rows := make([]display.Row, t.Height())
	for y := range rows {
		row, err := displayRow(t.Width(), func(x int) *uv.Cell { return t.CellAt(x, y) })
		rows[y] = row
		resultErr = errors.Join(resultErr, err)
		if len(row.Cells) > 0 {
			end = max(end, y+1)
		}
	}
	doc.Rows = append(doc.Rows, rows[:min(end, len(rows))]...)
	return doc, resultErr
}

func displayRow(columns int, at func(int) *uv.Cell) (display.Row, error) {
	var row display.Row
	for x := 0; x < columns; {
		cell := display.Cell{Text: " ", Width: 1}
		if c := at(x); c != nil {
			cell.Style = displayStyle(c.Style)
			if c.Content != "" {
				if c.Width < 1 || c.Width > 2 || x+c.Width > columns ||
					!utf8.ValidString(c.Content) || strings.ContainsFunc(c.Content, unicode.IsControl) {
					return row, fmt.Errorf("表示セルを共有形式に変換できません: column=%d width=%d", x, c.Width)
				}
				cell.Text, cell.Width = c.Content, c.Width
			}
			if c.Style.Attrs&uv.AttrConceal != 0 {
				cell.Text = strings.Repeat(" ", cell.Width)
				if cell.Width == 2 {
					// 非表示属性は共有型にないため、同じ列数の空白として渡す。
					cell.Text, cell.Width = " ", 1
					row.Cells = append(row.Cells, cell)
					x++
				}
			}
		}
		row.Cells = append(row.Cells, cell)
		x += cell.Width
	}
	for len(row.Cells) > 0 {
		last := row.Cells[len(row.Cells)-1]
		if last.Text != " " || last.Style != (display.Style{}) {
			break
		}
		row.Cells = row.Cells[:len(row.Cells)-1]
	}
	if len(row.Cells) == 0 {
		row.Cells = nil
	} else if cap(row.Cells) > len(row.Cells) {
		cells := make([]display.Cell, len(row.Cells))
		copy(cells, row.Cells)
		row.Cells = cells
	}
	return row, nil
}

func displayStyle(s uv.Style) display.Style {
	return display.Style{
		Foreground: displayColor(s.Fg), Background: displayColor(s.Bg),
		Bold: s.Attrs&uv.AttrBold != 0, Faint: s.Attrs&uv.AttrFaint != 0,
		Italic: s.Attrs&uv.AttrItalic != 0, Underline: s.Underline != uv.UnderlineStyleNone,
		Strikethrough: s.Attrs&uv.AttrStrikethrough != 0, Inverse: s.Attrs&uv.AttrReverse != 0,
	}
}

func displayColor(c color.Color) display.Color {
	switch c := c.(type) {
	case nil:
		return display.Color{}
	case ansi.BasicColor:
		return display.Color{Kind: display.ColorIndexed, Index: uint8(c)}
	case ansi.IndexedColor:
		return display.Color{Kind: display.ColorIndexed, Index: uint8(c)}
	default:
		r, g, b, _ := c.RGBA()
		return display.Color{Kind: display.ColorRGB, R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
}
