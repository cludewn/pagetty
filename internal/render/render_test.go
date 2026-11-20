package render

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"github.com/cludewn/pagetty/internal/display"
)

func TestRenderCellPositions(t *testing.T) {
	green := display.Style{Foreground: display.Color{Kind: display.ColorRGB, G: 255}}
	doc := display.Document{Columns: 13}
	for _, text := range []string{strings.Repeat("A", 12), strings.Repeat("日", 6), "AéBCDEFGHIJK", "AéBCDEFGHIJK", strings.Repeat("A", 6)} {
		var row display.Row
		for _, r := range text {
			if r == '\u0301' {
				row.Cells[len(row.Cells)-1].Text += string(r)
				continue
			}
			width := 1
			if r == '日' || len(doc.Rows) == 4 {
				width = 2
			}
			row.Cells = append(row.Cells, display.Cell{Text: string(r), Width: width})
		}
		row.Cells = append(row.Cells, display.Cell{Text: "|", Width: 1, Style: green})
		doc.Rows = append(doc.Rows, row)
	}
	before := fmt.Sprintf("%#v", doc)
	images, err := Render(doc, Options{MaxLines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Format != PNG || images[0].Width != 162 || images[0].Height != 172 {
		t.Fatalf("unexpected image metadata: %+v", images)
	}
	img := decodePNG(t, images[0])
	var marker image.Rectangle
	for row := range doc.Rows {
		bounds := image.Rectangle{}
		for y := padding + row*lineHeight; y < padding+(row+1)*lineHeight; y++ {
			for x := padding; x < img.Bounds().Dx()-padding; x++ {
				c := rgba(img.At(x, y))
				if int(c.G)-max(int(c.R), int(c.B)) > 60 {
					point := image.Rect(x, y-row*lineHeight, x+1, y-row*lineHeight+1)
					bounds = bounds.Union(point)
				}
			}
		}
		if bounds.Empty() || bounds.Min.X < padding+12*cellWidth || bounds.Max.X > padding+13*cellWidth {
			t.Fatalf("row %d: marker outside column 12: %v", row, bounds)
		}
		if row == 0 {
			marker = bounds
		} else if bounds != marker {
			t.Errorf("row %d marker = %v, ASCII marker = %v", row, bounds, marker)
		}
	}
	for y := 0; y < lineHeight; y++ {
		for x := padding; x < padding+12*cellWidth; x++ {
			a, b := rgba(img.At(x, padding+2*lineHeight+y)), rgba(img.At(x, padding+3*lineHeight+y))
			if diff := int(a.R) - int(b.R); diff < -2 || 2 < diff {
				t.Fatal("combining and precomposed characters differ beyond antialiasing tolerance")
			}
		}
	}
	if fmt.Sprintf("%#v", doc) != before {
		t.Error("input changed")
	}
}

func TestRenderColorsAcrossPages(t *testing.T) {
	rgb := func(r, g, b uint8) display.Color { return display.Color{Kind: display.ColorRGB, R: r, G: g, B: b} }
	indexed := func(i uint8) display.Color { return display.Color{Kind: display.ColorIndexed, Index: i} }
	tests := []struct {
		style  display.Style
		fg, bg color.RGBA
	}{
		{display.Style{}, color.RGBA{220, 220, 220, 255}, color.RGBA{23, 23, 23, 255}},
		{display.Style{Foreground: rgb(0, 0, 0)}, color.RGBA{0, 0, 0, 255}, color.RGBA{23, 23, 23, 255}},
		{display.Style{Foreground: rgb(200, 100, 50), Background: rgb(12, 34, 56)}, color.RGBA{200, 100, 50, 255}, color.RGBA{12, 34, 56, 255}},
		{display.Style{Foreground: indexed(1), Background: indexed(4), Bold: true}, color.RGBA{128, 0, 0, 255}, color.RGBA{0, 0, 128, 255}},
		{display.Style{Foreground: indexed(196), Background: indexed(232)}, color.RGBA{255, 0, 0, 255}, color.RGBA{8, 8, 8, 255}},
		{display.Style{Foreground: rgb(200, 100, 50), Background: rgb(12, 34, 56), Faint: true}, color.RGBA{106, 67, 53, 255}, color.RGBA{12, 34, 56, 255}},
		{display.Style{Inverse: true}, color.RGBA{23, 23, 23, 255}, color.RGBA{220, 220, 220, 255}},
		{display.Style{Foreground: rgb(200, 100, 50), Background: rgb(12, 34, 56), Inverse: true}, color.RGBA{12, 34, 56, 255}, color.RGBA{200, 100, 50, 255}},
		{display.Style{Foreground: indexed(1), Bold: true, Faint: true}, color.RGBA{75, 11, 11, 255}, color.RGBA{23, 23, 23, 255}},
		{display.Style{Foreground: indexed(231), Background: indexed(255)}, color.RGBA{255, 255, 255, 255}, color.RGBA{238, 238, 238, 255}},
		{display.Style{Foreground: indexed(9), Background: indexed(16)}, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 0, 255}},
	}
	doc := display.Document{Columns: 3}
	for _, tt := range tests {
		style := tt.style
		style.Underline, style.Strikethrough = true, true
		doc.Rows = append(doc.Rows, display.Row{Cells: []display.Cell{
			{Text: " ", Width: 1, Style: style}, {Text: " ", Width: 1},
		}})
	}
	images, err := Render(doc, Options{MaxLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 6 {
		t.Fatalf("got %d pages, want 6", len(images))
	}
	for page, result := range images {
		img := decodePNG(t, result)
		rows := 2
		if page == 5 {
			rows = 1
		}
		if result.Width != 62 || result.Height != 32+rows*28 {
			t.Errorf("page %d dimensions = %dx%d", page, result.Width, result.Height)
		}
		for row := 0; row < rows; row++ {
			tt := tests[page*2+row]
			for _, point := range []struct {
				x, y int
				want color.RGBA
			}{
				{padding + 5, padding + row*28 + 10, tt.bg},
				{padding + 5, padding + row*28 + 23, tt.fg},
				{padding + 5, padding + row*28 + 13, tt.fg},
				{padding + 15, padding + row*28 + 10, defaultBackground},
				{padding + 25, padding + row*28 + 10, defaultBackground},
			} {
				if got := rgba(img.At(point.x, point.y)); got != point.want {
					t.Errorf("page %d row %d pixel (%d,%d) = %v, want %v", page, row, point.x, point.y, got, point.want)
				}
			}
		}
	}
}

func TestRenderBoldAndItalic(t *testing.T) {
	doc := display.Document{Columns: 2}
	for _, style := range []display.Style{{}, {Bold: true}, {Italic: true}, {Bold: true, Italic: true}} {
		doc.Rows = append(doc.Rows, display.Row{Cells: []display.Cell{{Text: "H", Width: 1, Style: style}}})
	}
	images, err := Render(doc, Options{MaxLines: 4})
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, images[0])
	var rows [4][]color.RGBA
	var ink [4]int
	for row := range rows {
		for y := padding + row*lineHeight; y < padding+(row+1)*lineHeight; y++ {
			for x := padding; x < padding+2*cellWidth; x++ {
				c := rgba(img.At(x, y))
				rows[row] = append(rows[row], c)
				if c.R > 100 {
					ink[row]++
				}
			}
		}
	}
	if ink[0] == 0 || ink[1] <= ink[0] || ink[3] <= ink[2] {
		t.Errorf("bold must have more ink: %v", ink)
	}
	if reflect.DeepEqual(rows[0], rows[2]) || reflect.DeepEqual(rows[1], rows[3]) {
		t.Error("italic has no effect")
	}
}

func TestRenderSVGAndPagination(t *testing.T) {
	doc := numberedDocument(9)
	doc.Rows[2] = display.Row{}
	for _, format := range []Format{PNG, SVG} {
		images, err := Render(doc, Options{MaxLines: 4, Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if len(images) != 3 {
			t.Fatalf("got %d pages", len(images))
		}
		for page, rows := range []int{3, 4, 2} {
			img := images[page]
			if img.Format != format || img.Width != 72 || img.Height != 32+28*rows {
				t.Fatalf("page %d: unexpected metadata: %+v", page, img)
			}
			if format == PNG {
				decodePNG(t, img)
				continue
			}
			var root struct {
				XMLName xml.Name
				Width   string   `xml:"width,attr"`
				Height  string   `xml:"height,attr"`
				ViewBox string   `xml:"viewBox,attr"`
				Paths   []string `xml:"path"`
				Text    []string `xml:"text"`
			}
			if err := xml.Unmarshal(img.Data, &root); err != nil {
				t.Fatal(err)
			}
			if root.XMLName.Space != "http://www.w3.org/2000/svg" || root.XMLName.Local != "svg" || root.Width != "72px" || root.Height != fmt.Sprintf("%dpx", img.Height) || root.ViewBox != fmt.Sprintf("0 0 72 %d", img.Height) {
				t.Errorf("invalid SVG root: %+v", root)
			}
			if len(root.Paths) < rows || len(root.Text) != 0 || bytes.Contains(img.Data, []byte("@font-face")) {
				t.Error("expected self-contained glyph paths")
			}
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	images, err := Render(display.Document{Columns: 4}, Options{MaxLines: 4})
	if err != nil || len(images) != 0 {
		t.Fatalf("empty document: %v, %v", images, err)
	}
	images, err = Render(display.Document{Columns: 4, Rows: []display.Row{{}}}, Options{MaxLines: 4})
	if err != nil || len(images) != 1 {
		t.Fatalf("blank row: %v, %v", images, err)
	}
	img := decodePNG(t, images[0])
	if img.Bounds() != image.Rect(0, 0, 72, 60) || rgba(img.At(20, 20)) != defaultBackground {
		t.Error("blank row not preserved")
	}
}

func TestRenderErrors(t *testing.T) {
	valid := display.Cell{Text: "A", Width: 1}
	for _, tt := range []struct {
		name string
		cell display.Cell
	}{
		{"zero width", display.Cell{Text: "A"}},
		{"wide width", display.Cell{Text: "A", Width: 3}},
		{"empty text", display.Cell{Width: 1}},
		{"invalid UTF-8", display.Cell{Text: "\xff", Width: 1}},
		{"newline", display.Cell{Text: "\n", Width: 1}},
		{"escape", display.Cell{Text: "\x1b", Width: 1}},
		{"line separator", display.Cell{Text: "\u2028", Width: 1}},
		{"two graphemes", display.Cell{Text: "AB", Width: 1}},
		{"bad foreground", display.Cell{Text: "A", Width: 1, Style: display.Style{Foreground: display.Color{Kind: 3}}}},
		{"bad background", display.Cell{Text: "A", Width: 1, Style: display.Style{Background: display.Color{Kind: 3}}}},
		{"missing glyph on later page", display.Cell{Text: "😀", Width: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := display.Document{Columns: 2, Rows: []display.Row{{Cells: []display.Cell{valid}}, {Cells: []display.Cell{tt.cell}}}}
			images, err := Render(doc, Options{MaxLines: 1})
			if err == nil || images != nil {
				t.Fatal("expected an error and no partial images")
			}
		})
	}
	for _, tt := range []struct {
		name    string
		doc     display.Document
		options Options
	}{
		{"invalid columns", display.Document{}, Options{MaxLines: 4}},
		{"invalid max lines", numberedDocument(1), Options{}},
		{"invalid format", numberedDocument(1), Options{MaxLines: 4, Format: "jpg"}},
		{"row overflow", display.Document{Columns: 1, Rows: []display.Row{{Cells: []display.Cell{valid, valid}}}}, Options{MaxLines: 4}},
		{"huge width", display.Document{Columns: int(^uint(0) >> 1), Rows: []display.Row{{}}}, Options{MaxLines: 4}},
		{"huge height", display.Document{Columns: 1, Rows: make([]display.Row, 1200)}, Options{MaxLines: 1200}},
		{"huge area", display.Document{Columns: 3000, Rows: make([]display.Row, 40)}, Options{MaxLines: 40}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			images, err := Render(tt.doc, tt.options)
			if err == nil || images != nil {
				t.Fatal("expected an error and no images")
			}
		})
	}
}

var errWrite = errors.New("write failed")

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestEncodeFailure(t *testing.T) {
	c := Image{Width: 10, Height: 10, Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`)}
	for _, format := range []Format{PNG, SVG} {
		if err := encode(failedWriter{}, c, format); !errors.Is(err, errWrite) {
			t.Errorf("%s encode error = %v, want write error", format, err)
		}
	}
	if err := encodePNG(&bytes.Buffer{}, []byte("<svg><path"), 10, 10); err == nil {
		t.Error("invalid SVG must fail conversion")
	}
	if _, err := loadFont([]byte("invalid font")); err == nil {
		t.Error("invalid font must fail loading")
	}
}

func decodePNG(t *testing.T, result Image) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(result.Data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != result.Width || img.Bounds().Dy() != result.Height {
		t.Fatal("PNG dimensions differ from metadata")
	}
	return img
}

func rgba(c color.Color) color.RGBA { return color.RGBAModel.Convert(c).(color.RGBA) }
