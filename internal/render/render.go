package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"unicode"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"

	"github.com/cludewn/pagetty/internal/display"
)

type Format string

const (
	PNG Format = "png"
	SVG Format = "svg"
)

type Options struct {
	MaxLines int    // 正の最大表示行数。
	Format   Format // 空ならPNG。
}

type Image struct {
	Format        Format
	Width, Height int // ピクセル単位。
	Data          []byte
}

const (
	cellWidth  = 10
	lineHeight = 28
	fontSize   = 20
	baseline   = 21
	padding    = 16
	maxSide    = 32768
	maxPixels  = 16 * 1024 * 1024
)

//go:embed fonts/BIZUDGothic-Regular.ttf
var regularFont []byte

//go:embed fonts/BIZUDGothic-Bold.ttf
var boldFont []byte

// Render は分割した各ページの画像を返す。保存は行わず、失敗時は画像を返さない。
func Render(doc display.Document, options Options) ([]Image, error) {
	format := options.Format
	if format == "" {
		format = PNG
	}
	if format != PNG && format != SVG {
		return nil, fmt.Errorf("unsupported image format: %q", format)
	}
	pages, err := Split(doc, options.MaxLines)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, nil
	}
	if err := validate(doc); err != nil {
		return nil, err
	}
	regular, err := loadFont(regularFont)
	if err != nil {
		return nil, fmt.Errorf("load regular font: %w", err)
	}
	bold, err := loadFont(boldFont)
	if err != nil {
		return nil, fmt.Errorf("load bold font: %w", err)
	}
	var images []Image
	for i, page := range pages {
		result, err := drawPage(page, regular, bold)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		var data bytes.Buffer
		if err := encode(&data, result, format); err != nil {
			return nil, fmt.Errorf("page %d: encode %s: %w", i+1, format, err)
		}
		result.Format, result.Data = format, data.Bytes()
		images = append(images, result)
	}
	return images, nil
}

func validate(doc display.Document) error {
	var segments segmenter.Segmenter
	for i, row := range doc.Rows {
		column := 0
		for j, cell := range row.Cells {
			invalid := func(reason string) error {
				return fmt.Errorf("row %d cell %d: %s", i+1, j+1, reason)
			}
			if cell.Width != 1 && cell.Width != 2 {
				return invalid("width must be 1 or 2")
			}
			if doc.Columns-cell.Width < column {
				return invalid("row exceeds document columns")
			}
			column += cell.Width
			if cell.Text == "" || !utf8.ValidString(cell.Text) {
				return invalid("text must be nonempty UTF-8")
			}
			for _, r := range cell.Text {
				if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
					return invalid("text contains a control character or line separator")
				}
			}
			segments.Init([]rune(cell.Text))
			graphemes := segments.GraphemeIterator()
			if !graphemes.Next() || graphemes.Next() {
				return invalid("text must contain one grapheme cluster")
			}
			if cell.Style.Foreground.Kind > display.ColorRGB || cell.Style.Background.Kind > display.ColorRGB {
				return invalid("unknown color kind")
			}
		}
	}
	return nil
}

type glyphKey struct {
	text string
	bold bool
}

func drawPage(doc display.Document, regular, bold *loadedFont) (Image, error) {
	// SVGでも同じ上限にし、PNG化で過大なメモリを確保しない。
	if doc.Columns > (maxSide-2*padding)/cellWidth || len(doc.Rows) > (maxSide-2*padding)/lineHeight {
		return Image{}, fmt.Errorf("image dimensions exceed %d pixels", maxSide)
	}
	w, h := doc.Columns*cellWidth+2*padding, len(doc.Rows)*lineHeight+2*padding
	if maxPixels/w < h {
		return Image{}, fmt.Errorf("image exceeds %d pixels", maxPixels)
	}
	var data bytes.Buffer
	fmt.Fprintf(&data, `<svg xmlns="http://www.w3.org/2000/svg" width="%dpx" height="%dpx" viewBox="0 0 %d %d">`, w, h, w, h)
	rect(&data, 0, 0, w, h, defaultBackground)
	paths := make(map[glyphKey]glyphPath)
	for i, row := range doc.Rows {
		column := 0
		y := padding + i*lineHeight
		// 後続セルの背景で斜体や結合文字の端を消さないよう、背景を先に描く。
		for _, cell := range row.Cells {
			_, bg := colors(cell.Style)
			rect(&data, padding+column*cellWidth, y, cell.Width*cellWidth, lineHeight, bg)
			column += cell.Width
		}
		column = 0
		for j, cell := range row.Cells {
			key := glyphKey{cell.Text, cell.Style.Bold}
			path, ok := paths[key]
			if !ok {
				font := regular
				if cell.Style.Bold {
					font = bold
				}
				var err error
				path, err = font.path(cell.Text)
				if err != nil {
					return Image{}, fmt.Errorf("row %d cell %d: %w", i+1, j+1, err)
				}
				paths[key] = path
			}
			fg, _ := colors(cell.Style)
			x, textY := padding+column*cellWidth, y+baseline
			sx := 1.0
			// 字形がセル幅を超える場合だけ縮め、後続セルの位置はWidthで決める。
			if width := float64(cell.Width * cellWidth); width < path.advance {
				sx = width / path.advance
			}
			slant := 0.0
			if cell.Style.Italic {
				slant = -0.3 * sx
			}
			fmt.Fprintf(&data, `<path fill="#%02x%02x%02x" d="%s" transform="matrix(%g 0 %g 1 %d %d)"/>`, fg.R, fg.G, fg.B, path.data, sx, slant, x, textY)
			if cell.Style.Underline {
				rect(&data, x, textY+2, cell.Width*cellWidth, 1, fg)
			}
			if cell.Style.Strikethrough {
				rect(&data, x, textY-8, cell.Width*cellWidth, 1, fg)
			}
			column += cell.Width
		}
	}
	data.WriteString(`</svg>`)
	return Image{Format: SVG, Width: w, Height: h, Data: data.Bytes()}, nil
}

func rect(w *bytes.Buffer, x, y, width, height int, c color.RGBA) {
	fmt.Fprintf(w, `<rect x="%d" y="%d" width="%d" height="%d" fill="#%02x%02x%02x"/>`, x, y, width, height, c.R, c.G, c.B)
}

func encode(w io.Writer, svg Image, format Format) error {
	if format == SVG {
		_, err := io.Copy(w, bytes.NewReader(svg.Data))
		return err
	}
	return encodePNG(w, svg.Data, svg.Width, svg.Height)
}

func encodePNG(w io.Writer, data []byte, width, height int) error {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.StrictErrorMode)
	if err != nil {
		return fmt.Errorf("parse generated SVG: %w", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	icon.SetTarget(0, 0, float64(width), float64(height))
	scanner := rasterx.NewScannerGV(width, height, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(width, height, scanner), 1)
	return png.Encode(w, img)
}
