package render

import (
	"fmt"

	"github.com/cludewn/pagetty/internal/display"
)

// Split は表示行を最大maxLines行のページへ分割する。返すスライスは読み取り専用。
// 内容がなければページを返さず、空のRowは一行として保持する。
func Split(doc display.Document, maxLines int) ([]display.Document, error) {
	if maxLines <= 0 {
		return nil, fmt.Errorf("max lines must be positive: %d", maxLines)
	}
	if doc.Columns <= 0 {
		return nil, fmt.Errorf("columns must be positive: %d", doc.Columns)
	}

	// 境界を戻す幅は最大3行、かつ最大行数の1/4までに留める。
	lookback := min(3, maxLines/4)
	var pages []display.Document
	for start := 0; start < len(doc.Rows); {
		end := start + min(maxLines, len(doc.Rows)-start)
		if end < len(doc.Rows) && !blank(doc.Rows[end-1]) {
			for candidate := end - 1; candidate >= end-lookback; candidate-- {
				if blank(doc.Rows[candidate-1]) {
					end = candidate
					break
				}
			}
		}
		pages = append(pages, display.Document{
			Columns: doc.Columns,
			Rows:    doc.Rows[start:end:end],
		})
		start = end
	}
	return pages, nil
}

func blank(row display.Row) bool {
	for _, cell := range row.Cells {
		if cell.Text != " " || cell.Style != (display.Style{}) {
			return false
		}
	}
	return true
}
