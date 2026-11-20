package render

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/cludewn/pagetty/internal/display"
)

func TestSplit(t *testing.T) {
	for _, tt := range []struct {
		name   string
		lines  int
		max    int
		blanks []int
		want   []int
	}{
		{name: "empty", max: 4},
		{name: "short", lines: 2, max: 4, want: []int{2}},
		{name: "exact", lines: 4, max: 4, want: []int{4}},
		{name: "one over", lines: 5, max: 4, want: []int{4, 1}},
		{name: "multiple", lines: 13, max: 4, want: []int{4, 4, 4, 1}},
		{name: "one per page", lines: 3, max: 1, want: []int{1, 1, 1}},
		{name: "near blank", lines: 9, max: 4, blanks: []int{2}, want: []int{3, 4, 2}},
		{name: "blank at boundary", lines: 9, max: 4, blanks: []int{2, 3}, want: []int{4, 4, 1}},
		{name: "far blank", lines: 9, max: 4, blanks: []int{1}, want: []int{4, 4, 1}},
		{name: "nearest blank", lines: 25, max: 12, blanks: []int{8, 9}, want: []int{10, 12, 3}},
		{name: "cap search at three", lines: 81, max: 40, blanks: []int{35}, want: []int{40, 40, 1}},
		{name: "final page untouched", lines: 4, max: 4, blanks: []int{2}, want: []int{4}},
		{name: "only empty row", lines: 1, max: 4, blanks: []int{0}, want: []int{1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := numberedDocument(tt.lines)
			for _, i := range tt.blanks {
				doc.Rows[i] = display.Row{}
			}
			before := fmt.Sprintf("%#v", doc)
			pages, err := Split(doc, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			var rows []display.Row
			for _, page := range pages {
				got = append(got, len(page.Rows))
				rows = append(rows, page.Rows...)
				if page.Columns != doc.Columns || len(page.Rows) == 0 || len(page.Rows) > tt.max {
					t.Fatalf("invalid page: %+v", page)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("page sizes = %v, want %v", got, tt.want)
			}
			if len(rows) != len(doc.Rows) || (len(rows) > 0 && !reflect.DeepEqual(rows, doc.Rows)) {
				t.Error("rows lost, duplicated or reordered")
			}
			if fmt.Sprintf("%#v", doc) != before {
				t.Error("input changed")
			}
		})
	}
}

func TestSplitPreservesCells(t *testing.T) {
	style := display.Style{
		Foreground: display.Color{Kind: display.ColorIndexed, Index: 1},
		Background: display.Color{Kind: display.ColorRGB, R: 12, G: 34, B: 56},
		Bold:       true, Faint: true, Italic: true, Underline: true, Strikethrough: true, Inverse: true,
	}
	doc := display.Document{Columns: 4, Rows: []display.Row{
		{Cells: []display.Cell{{Text: "日", Width: 2, Style: style}, {Text: "本", Width: 2, Style: style}}},
		{Cells: []display.Cell{{Text: "語", Width: 2, Style: style}, {Text: "e\u0301", Width: 1}, {Text: " ", Width: 1, Style: style}}},
	}}
	pages, err := Split(doc, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || !reflect.DeepEqual(pages[0].Rows[0], doc.Rows[0]) || !reflect.DeepEqual(pages[1].Rows[0], doc.Rows[1]) {
		t.Fatalf("cell positions or styles changed: %+v", pages)
	}
}

func TestBlank(t *testing.T) {
	for _, tt := range []struct {
		name string
		row  display.Row
		want bool
	}{
		{"empty", display.Row{}, true},
		{"spaces", display.Row{Cells: []display.Cell{{Text: " ", Width: 1}, {Text: " ", Width: 1}}}, true},
		{"background", display.Row{Cells: []display.Cell{{Text: " ", Width: 1, Style: display.Style{Background: display.Color{Kind: display.ColorIndexed, Index: 1}}}}}, false},
		{"underline", display.Row{Cells: []display.Cell{{Text: " ", Width: 1, Style: display.Style{Underline: true}}}}, false},
		{"wide space", display.Row{Cells: []display.Cell{{Text: "　", Width: 2}}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := blank(tt.row); got != tt.want {
				t.Errorf("blank = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSplitInvalidOptions(t *testing.T) {
	for _, tt := range []struct{ columns, max int }{{4, 0}, {4, -1}, {0, 4}, {-1, 4}} {
		pages, err := Split(display.Document{Columns: tt.columns}, tt.max)
		if err == nil || pages != nil {
			t.Fatalf("Split(%+v) = %v, %v; want no pages and an error", tt, pages, err)
		}
	}
}

func TestSplitLongOutput(t *testing.T) {
	doc := numberedDocument(1001)
	for i := 7; i < len(doc.Rows); i += 11 {
		doc.Rows[i] = display.Row{}
	}
	for maxLines := 1; maxLines <= 80; maxLines++ {
		pages, err := Split(doc, maxLines)
		if err != nil {
			t.Fatal(err)
		}
		next := 0
		for _, page := range pages {
			if len(page.Rows) == 0 || len(page.Rows) > maxLines {
				t.Fatalf("max %d: invalid page length %d", maxLines, len(page.Rows))
			}
			for _, row := range page.Rows {
				if next >= len(doc.Rows) || !reflect.DeepEqual(row, doc.Rows[next]) {
					t.Fatalf("max %d: row %d lost, duplicated or reordered", maxLines, next)
				}
				next++
			}
		}
		if next != len(doc.Rows) {
			t.Fatalf("max %d: got %d rows, want %d", maxLines, next, len(doc.Rows))
		}
	}
}

func numberedDocument(lines int) display.Document {
	doc := display.Document{Columns: 4}
	for i := 0; i < lines; i++ {
		var row display.Row
		for _, r := range fmt.Sprintf("%04d", i) {
			row.Cells = append(row.Cells, display.Cell{Text: string(r), Width: 1})
		}
		doc.Rows = append(doc.Rows, row)
	}
	return doc
}
