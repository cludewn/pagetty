package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"

	"github.com/cludewn/pagetty/internal/display"
)

func TestPNGMatchesFullPageRasterizer(t *testing.T) {
	for _, tt := range []struct {
		name          string
		columns, rows int
	}{
		{"fixed", 48, 16}, // 512 × 480
		{"float from width", 49, 16},
		{"float from height", 48, 20},
		{"report page", 80, 44},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := display.Document{Columns: tt.columns, Rows: make([]display.Row, tt.rows)}
			style := display.Style{
				Bold: true, Italic: true, Underline: true, Strikethrough: true,
				Background: display.Color{Kind: display.ColorRGB, R: 50, G: 30, B: 90},
			}
			for _, i := range []int{0, tt.rows / 2, tt.rows - 1} {
				doc.Rows[i].Cells = []display.Cell{
					{Text: "A", Width: 1},
					{Text: "日", Width: 2, Style: style},
					{Text: "é", Width: 1, Style: style},
					{Text: "Ａ", Width: 2},
					{Text: "🚀", Width: 2, Style: style},
					{Text: " ", Width: 1, Style: style},
					{Text: "|", Width: 1},
				}
			}
			svgs, err := Render(doc, Options{Format: SVG, MaxLines: tt.rows})
			if err != nil || len(svgs) != 1 {
				t.Fatalf("SVG pages=%d, error=%v", len(svgs), err)
			}
			svg := svgs[0]
			icon, err := oksvg.ReadIconStream(bytes.NewReader(svg.Data), oksvg.StrictErrorMode)
			if err != nil {
				t.Fatal(err)
			}
			w, h := svg.Width, svg.Height
			reference := image.NewRGBA(image.Rect(0, 0, w, h))
			icon.SetTarget(0, 0, float64(w), float64(h))
			icon.Draw(rasterx.NewDasher(w, h, rasterx.NewScannerGV(w, h, reference, reference.Bounds())), 1)
			var want, got bytes.Buffer
			if err := png.Encode(&want, reference); err != nil {
				t.Fatal(err)
			}
			if err := encodePNG(&got, svg.Data, w, h); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Error("PNG differs from the full-page rasterizer")
			}
		})
	}
}
