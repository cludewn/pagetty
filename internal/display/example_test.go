package display_test

import (
	"fmt"
	"strings"

	"github.com/cludewn/pagetty/internal/display"
)

func ExampleDocument() {
	red := display.Style{
		Foreground: display.Color{Kind: display.ColorIndexed, Index: 1},
		Bold:       true,
	}
	doc := display.Document{
		Columns: 4,
		Rows: []display.Row{
			// CR上書き後の表示だけを保持する。
			{Cells: []display.Cell{{Text: "O", Width: 1}, {Text: "K", Width: 1}}},
			// 空のRowも一行として数える。
			{},
			{Cells: []display.Cell{
				{Text: "日", Width: 2, Style: red},
				{Text: "本", Width: 2, Style: red},
			}},
			// 折り返し先にも完全な属性を持たせる。
			{Cells: []display.Cell{
				{Text: "語", Width: 2, Style: red},
				{Text: "e\u0301", Width: 1},
				{Text: " ", Width: 1, Style: display.Style{
					Background: display.Color{Kind: display.ColorRGB, R: 12, G: 34, B: 56},
				}},
			}},
		},
	}

	for i, row := range doc.Rows {
		var text strings.Builder
		column := 0
		for _, cell := range row.Cells {
			text.WriteString(cell.Text)
			column += cell.Width
		}
		fmt.Printf("row %d: %q (%d columns)\n", i, text.String(), column)
	}
	// ページ先頭でも前行のSGR状態は不要。
	page := doc.Rows[3:]
	fmt.Println("page starts red and bold:", page[0].Cells[0].Style == red)
	fmt.Println("next cell uses defaults:", page[0].Cells[1].Style == (display.Style{}))
	bg := page[0].Cells[2].Style.Background
	fmt.Printf("trailing space background: %d,%d,%d\n", bg.R, bg.G, bg.B)

	// Output:
	// row 0: "OK" (2 columns)
	// row 1: "" (0 columns)
	// row 2: "日本" (4 columns)
	// row 3: "語é " (4 columns)
	// page starts red and bold: true
	// next cell uses defaults: true
	// trailing space background: 12,34,56
}
