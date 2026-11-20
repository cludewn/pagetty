package capture

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/cludewn/pagetty/internal/display"
)

func rowTexts(doc display.Document) []string {
	rows := make([]string, len(doc.Rows))
	for i, row := range doc.Rows {
		var b strings.Builder
		for _, cell := range row.Cells {
			b.WriteString(cell.Text)
		}
		rows[i] = b.String()
	}
	return rows
}

func checkDocument(t *testing.T, doc display.Document) {
	t.Helper()
	for i, row := range doc.Rows {
		width := 0
		for _, cell := range row.Cells {
			if cell.Text == "" || cell.Width < 1 || cell.Width > 2 {
				t.Fatalf("invalid cell in row %d: %+v", i, cell)
			}
			width += cell.Width
		}
		if width > doc.Columns {
			t.Fatalf("row %d exceeds columns: %d > %d", i, width, doc.Columns)
		}
	}
}

func TestScreenRows(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		columns     int
		want        []string
	}{
		{"empty", "", 8, []string{}},
		{"newlines", "a\r\n\r\nb\r\n", 8, []string{"a", "", "b"}},
		{"only blank lines", "\r\n\r\n", 8, []string{"", ""}},
		{"unterminated", "last", 8, []string{"last"}},
		{"CR retains suffix", "12345\rOK", 8, []string{"OK345"}},
		{"CR erase", "12345\rOK\x1b[K", 8, []string{"OK"}},
		{"cursor movement", "abc\x1b[2DX", 8, []string{"aXc"}},
		{"wrap", "abcdefghi", 4, []string{"abcd", "efgh", "i"}},
		{"wide wrap", "日本語", 4, []string{"日本", "語"}},
		{"wide before edge", "abc日x", 4, []string{"abc", "日x"}},
		{"combining", "e\u0301か\u3099", 8, []string{"éが"}},
		{"tab", "a\tb", 16, []string{"a       b"}},
		{"queries", "a\x1b[6nb", 8, []string{"ab"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 読み取り境界がUTF-8・結合文字・ANSIシーケンスを分断しても同じ表示になる。
			for split := 0; split <= len(tc.input); split++ {
				s := newScreen(tc.columns, 3)
				_, _ = s.Write([]byte(tc.input[:split]))
				_, _ = s.Write([]byte(tc.input[split:]))
				doc, err := s.document()
				_ = s.terminal.Close()
				if err != nil {
					t.Fatal(err)
				}
				checkDocument(t, doc)
				if got := rowTexts(doc); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("split %d: got %q, want %q", split, got, tc.want)
				}
			}
		})
	}
}

func TestScreenScrollback(t *testing.T) {
	s := newScreen(12, 3)
	defer s.terminal.Close()
	const count = 10050
	for i := range count {
		if _, err := fmt.Fprintf(s, "%05d\r\n", i); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := s.document()
	if err != nil {
		t.Fatal(err)
	}
	checkDocument(t, doc)
	if len(doc.Rows) != count {
		t.Fatalf("rows=%d, want %d", len(doc.Rows), count)
	}
	for i, text := range rowTexts(doc) {
		if want := fmt.Sprintf("%05d", i); text != want {
			t.Fatalf("row %d=%q, want %q", i, text, want)
		}
	}
}

func TestScreenStyle(t *testing.T) {
	s := newScreen(8, 2)
	defer s.terminal.Close()
	_, _ = s.Write([]byte("\x1b[1;2;3;4;7;9;31;48;2;12;34;56ma\r\nb\x1b[0mc"))
	doc, err := s.document()
	if err != nil {
		t.Fatal(err)
	}
	want := display.Style{
		Foreground: display.Color{Kind: display.ColorIndexed, Index: 1},
		Background: display.Color{Kind: display.ColorRGB, R: 12, G: 34, B: 56},
		Bold:       true, Faint: true, Italic: true, Underline: true, Inverse: true, Strikethrough: true,
	}
	if doc.Rows[0].Cells[0].Style != want || doc.Rows[1].Cells[0].Style != want {
		t.Fatalf("attribute continuation: %+v", doc)
	}
	if got := doc.Rows[1].Cells[1].Style; got != (display.Style{}) {
		t.Fatalf("reset: %+v", got)
	}

	_, _ = s.Write([]byte("\x1b[38;5;0;48;5;200m \x1b[0m"))
	doc, err = s.document()
	if err != nil {
		t.Fatal(err)
	}
	cell := doc.Rows[1].Cells[2]
	if cell.Text != " " || cell.Style.Foreground != (display.Color{Kind: display.ColorIndexed, Index: 0}) ||
		cell.Style.Background != (display.Color{Kind: display.ColorIndexed, Index: 200}) {
		t.Fatalf("styled trailing blank or indexed black lost: %+v", cell)
	}
}

func TestScreenStyledEraseAndConceal(t *testing.T) {
	s := newScreen(8, 3)
	defer s.terminal.Close()
	_, _ = s.Write([]byte("a\x1b[44m\x1b[K\x1b[0m\r\n\x1b[8m秘密\x1b[0m!"))
	doc, err := s.document()
	if err != nil {
		t.Fatal(err)
	}
	checkDocument(t, doc)
	if got := rowTexts(doc); !reflect.DeepEqual(got, []string{"a       ", "    !"}) {
		t.Fatalf("got %q", got)
	}
	if doc.Rows[0].Cells[7].Style.Background != (display.Color{Kind: display.ColorIndexed, Index: 4}) {
		t.Fatal("erased background lost")
	}
}

func TestDisplayRowStorage(t *testing.T) {
	for _, text := range []string{"", "x", "日e\u0301"} {
		t.Run(text, func(t *testing.T) {
			s := newScreen(200, 2)
			defer s.terminal.Close()
			_, _ = s.Write([]byte(text))
			row, err := displayRow(200, func(x int) *uv.Cell { return s.terminal.CellAt(x, 0) })
			if err != nil {
				t.Fatal(err)
			}
			if cap(row.Cells) != len(row.Cells) || (text == "" && row.Cells != nil) {
				t.Fatalf("retained cell storage: len=%d cap=%d nil=%t", len(row.Cells), cap(row.Cells), row.Cells == nil)
			}
			if got := strings.Join(rowTexts(display.Document{Rows: []display.Row{row}}), ""); got != text {
				t.Fatalf("text=%q, want %q", got, text)
			}
		})
	}
}
