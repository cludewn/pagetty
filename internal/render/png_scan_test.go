package render

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"math"
	"testing"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"

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
			if err := encodePNG(context.Background(), &got, svg.Data, w, h); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Error("PNG differs from the full-page rasterizer")
			}
		})
	}
}

func TestPNGScannerClipsAtPageBounds(t *testing.T) {
	const height = 64
	for _, mode := range []struct {
		name                 string
		width, extremePixels int
	}{{"fixed", 32, 0}, {"float", 600, 944}} {
		t.Run(mode.name, func(t *testing.T) {
			for _, tt := range []struct {
				name        string
				top, bottom fixed.Int26_6
				pixels      int
			}{
				{"overflow", fixed.I(5), math.MaxInt32 - 16, mode.extremePixels},
				{"clip far below", fixed.I(5), fixed.I(1000), 944},
				{"clip both edges", fixed.I(-4), fixed.I(height + 4), 16 * height},
				{"at edges", 0, fixed.I(height), 16 * height},
				{"outside above", fixed.I(-4), 0, 0},
				{"outside below", fixed.I(height), fixed.I(height + 5), 0},
				{"inside", fixed.I(6), fixed.I(15), 16 * 9},
			} {
				t.Run(tt.name, func(t *testing.T) {
					bounds := image.Rect(0, 0, mode.width, height)
					want, got := image.NewRGBA(bounds), image.NewRGBA(bounds)
					legacy := rasterx.NewScannerGV(mode.width, height, want, bounds)
					for _, scanner := range []rasterx.Scanner{legacy, newPNGScanner(got)} {
						point := func(x int, y fixed.Int26_6) fixed.Point26_6 {
							return fixed.Point26_6{X: fixed.I(x), Y: y}
						}
						scanner.Start(point(8, tt.top))
						scanner.Line(point(24, tt.top))
						scanner.Line(point(24, tt.bottom))
						scanner.Line(point(8, tt.bottom))
						scanner.Line(point(8, tt.top))
						scanner.Draw()
					}
					count := 0
					for i := 3; i < len(want.Pix); i += 4 {
						if want.Pix[i] != 0 {
							count++
						}
					}
					if count != tt.pixels {
						t.Fatalf("reference drew %d pixels, want %d", count, tt.pixels)
					}
					if !bytes.Equal(want.Pix, got.Pix) {
						t.Errorf("scan band differs from reference (%d visible pixels)", count)
					}
				})
			}
		})
	}
}
