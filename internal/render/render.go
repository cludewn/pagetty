package render

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
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
	PageLimit   int
	MaxLines    int                                                      // 正の最大表示行数。
	Format      Format                                                   // 空ならPNG。
	ChoosePages func(total, available int, hardLimit Limit) (int, error) // 設定超過時に希望枚数を返す。0は保存しない。
	Progress    func(done, total int)                                    // 保存範囲の確定後と各ページ生成後に同期的に呼ぶ。
}

type Image struct {
	Format        Format
	Width, Height int // ピクセル単位。
	Data          []byte
}

const (
	cellWidth        = 10
	lineHeight       = 28
	fontSize         = 20
	baseline         = 21
	padding          = 16
	maxSide          = 32768
	maxPixels        = 16 * 1024 * 1024
	MaxColumns       = (maxSide - 2*padding) / cellWidth
	DefaultPageLimit = 16
	MaxPages         = 64
	maxPNGPixels     = 32 * 1024 * 1024
	maxSVGBytes      = 16 * 1024 * 1024
	maxImageBytes    = 8 * 1024 * 1024
	maxTotalBytes    = 64 * 1024 * 1024
)

//go:embed fonts/BIZUDGothic-Regular.ttf
var regularFont []byte

//go:embed fonts/BIZUDGothic-Bold.ttf
var boldFont []byte

// PageLines は指定した最大行数を画像の寸法制限内に収める。
func PageLines(columns, lines int) (int, error) {
	if columns < 1 || columns > MaxColumns || lines < 1 {
		return 0, fmt.Errorf("invalid columns or lines: %d, %d", columns, lines)
	}
	width := columns*cellWidth + 2*padding
	return min(lines, (min(maxSide, maxPixels/width)-2*padding)/lineHeight), nil
}

type Limit string

func (l Limit) Error() string { return string(l) }

const (
	limitPages       Limit = "最大生成ページ数"
	limitPageSetting Limit = "設定上限のページ数"
	limitPixels      Limit = "PNGの合計描画画素数"
	limitSVG         Limit = "内部SVGのサイズ"
	limitImage       Limit = "画像1枚のサイズ"
	limitTotal       Limit = "画像の合計サイズ"
)

// RenderPages は空行を考慮したページを先頭から生成し、各画像を同期的に渡す。
func RenderPages(ctx context.Context, doc display.Document, options Options, emit func(Image) error) (Limit, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	format := options.Format
	if format == "" {
		format = PNG
	}
	if format != PNG && format != SVG {
		return "", fmt.Errorf("unsupported image format: %q", format)
	}
	lines, err := PageLines(doc.Columns, options.MaxLines)
	if err != nil {
		return "", err
	}
	if options.PageLimit < 1 || options.PageLimit > MaxPages {
		return "", fmt.Errorf("invalid page limit: %d", options.PageLimit)
	}
	pages, err := split(ctx, doc, lines)
	if err != nil || len(pages) == 0 {
		return "", err
	}
	available, hardLimit := availablePages(pages, format)
	count := min(len(pages), options.PageLimit)
	var limit Limit
	if len(pages) > options.PageLimit {
		if options.ChoosePages == nil {
			limit = limitPageSetting
		} else {
			count, err = options.ChoosePages(len(pages), available, hardLimit)
			if err != nil {
				return "", err
			}
			if count < 0 || count > len(pages) {
				return "", fmt.Errorf("invalid selected page count: %d", count)
			}
		}
	}
	if count > available {
		count, limit = available, hardLimit
	}
	if count == 0 {
		return limit, ctx.Err()
	}
	pages = pages[:count]
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if options.Progress != nil {
		options.Progress(0, len(pages))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	regular, err := loadFont(regularFont)
	if err != nil {
		return "", fmt.Errorf("load regular font: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	bold, err := loadFont(boldFont)
	if err != nil {
		return "", fmt.Errorf("load bold font: %w", err)
	}
	var size int
	for i, page := range pages {
		if err := validate(ctx, page); err != nil {
			return "", fmt.Errorf("page %d: %w", i+1, err)
		}
		img, err := drawPage(ctx, page, regular, bold, format, maxTotalBytes-size)
		if err != nil {
			var reached Limit
			if errors.As(err, &reached) {
				return reached, nil
			}
			return "", fmt.Errorf("page %d: %w", i+1, err)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := emit(img); err != nil {
			return limit, err
		}
		if options.Progress != nil {
			options.Progress(i+1, len(pages))
		}
		size += len(img.Data)
	}
	return limit, ctx.Err()
}

// 画像を描画する前に確定できる制限だけを調べる。
func availablePages(pages []display.Document, format Format) (int, Limit) {
	count := min(len(pages), MaxPages)
	if format == PNG {
		remaining := maxPNGPixels
		for i, page := range pages[:count] {
			width, height := page.Columns*cellWidth+2*padding, len(page.Rows)*lineHeight+2*padding
			remaining -= width * height
			if remaining < 0 {
				return i, limitPixels
			}
		}
	}
	if count < len(pages) {
		return count, limitPages
	}
	return count, ""
}

func validate(ctx context.Context, doc display.Document) error {
	var segments segmenter.Segmenter
	for i, row := range doc.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
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

func drawPage(ctx context.Context, doc display.Document, regular, bold *loadedFont, format Format, remaining int) (Image, error) {
	w, h := doc.Columns*cellWidth+2*padding, len(doc.Rows)*lineHeight+2*padding
	finalBytes := min(maxImageBytes, remaining)
	finalLimit := limitImage
	if remaining < maxImageBytes {
		finalLimit = limitTotal
	}
	data := pageBuffer{ctx: ctx, maxBytes: maxSVGBytes, limit: limitSVG}
	if format == SVG {
		data.maxBytes, data.limit = finalBytes, finalLimit
	}
	fmt.Fprintf(&data, `<svg xmlns="http://www.w3.org/2000/svg" width="%dpx" height="%dpx" viewBox="0 0 %d %d">`, w, h, w, h)
	rect(&data, 0, 0, w, h, defaultBackground)
	paths := make(map[glyphKey]glyphPath)
	for i, row := range doc.Rows {
		if data.err != nil {
			return Image{}, data.err
		}
		if err := ctx.Err(); err != nil {
			return Image{}, err
		}
		column := 0
		y := padding + i*lineHeight
		// 後続セルの背景で斜体や結合文字の端を消さないよう、背景を先に描く。
		var start int
		var background color.RGBA
		for _, cell := range row.Cells {
			_, bg := colors(cell.Style)
			if bg != background {
				if column > start {
					rect(&data, padding+start*cellWidth, y, (column-start)*cellWidth, lineHeight, background)
				}
				background, start = bg, column
			}
			column += cell.Width
		}
		if column > start {
			rect(&data, padding+start*cellWidth, y, (column-start)*cellWidth, lineHeight, background)
		}
		column = 0
		for j, cell := range row.Cells {
			if data.err != nil {
				return Image{}, data.err
			}
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
			if path.data != "" {
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
			}
			if cell.Style.Underline {
				rect(&data, x, textY+2, cell.Width*cellWidth, 1, fg)
			}
			if cell.Style.Strikethrough {
				rect(&data, x, textY-8, cell.Width*cellWidth, 1, fg)
			}
			column += cell.Width
		}
	}
	fmt.Fprint(&data, "</svg>")
	if data.err != nil {
		return Image{}, data.err
	}
	if format == SVG {
		return Image{Format: format, Width: w, Height: h, Data: data.buf}, nil
	}
	encoded := pageBuffer{ctx: ctx, maxBytes: finalBytes, limit: finalLimit}
	if err := encodePNG(ctx, &encoded, data.buf, w, h); err != nil {
		return Image{}, err
	}
	return Image{Format: format, Width: w, Height: h, Data: encoded.buf}, nil
}

type pageBuffer struct {
	buf      []byte
	ctx      context.Context
	maxBytes int
	limit    Limit
	err      error
}

func (b *pageBuffer) Write(p []byte) (int, error) {
	if b.err == nil {
		b.err = b.ctx.Err()
	}
	if b.err == nil && len(p) > b.maxBytes-len(b.buf) {
		b.err = b.limit
	}
	if b.err != nil {
		return 0, b.err
	}
	if needed := len(b.buf) + len(p); needed > cap(b.buf) {
		grown := make([]byte, len(b.buf), min(b.maxBytes, max(needed, 2*cap(b.buf), 512)))
		copy(grown, b.buf)
		b.buf = grown
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func rect(w io.Writer, x, y, width, height int, c color.RGBA) {
	fmt.Fprintf(w, `<rect x="%d" y="%d" width="%d" height="%d" fill="#%02x%02x%02x"/>`, x, y, width, height, c.R, c.G, c.B)
}

type svgReader struct {
	ctx    context.Context
	reader *bytes.Reader
}

func (r svgReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func encodePNG(ctx context.Context, w io.Writer, data []byte, width, height int) error {
	icon, err := oksvg.ReadIconStream(svgReader{ctx, bytes.NewReader(data)}, oksvg.StrictErrorMode)
	if err != nil {
		return fmt.Errorf("parse generated SVG: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	icon.SetTarget(0, 0, float64(width), float64(height))
	scanner := newPNGScanner(img)
	dasher := rasterx.NewDasher(width, height, scanner)
	for _, path := range icon.SVGPaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		path.DrawTransformed(dasher, 1, icon.Transform)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := encodePixels(ctx, w, img); err != nil {
		return err
	}
	return ctx.Err()
}

// png.Encodeには行境界のキャンセルAPIがない。高圧縮時にも
// 書き込みを待たずに抜けるため、行の先頭で専用の中断値を返す。
type pngCanceled struct{ err error }

type interruptibleImage struct {
	*image.RGBA
	ctx   context.Context
	pixel color.RGBA
}

func (m *interruptibleImage) At(x, y int) color.Color {
	if x == m.Rect.Min.X {
		if err := m.ctx.Err(); err != nil {
			panic(pngCanceled{err})
		}
	}
	// エンコーダーはAtの戻り値を同期的に色へ変換する。
	m.pixel = m.RGBAAt(x, y)
	return &m.pixel
}

func encodePixels(ctx context.Context, w io.Writer, img *image.RGBA) (err error) {
	if ctx.Done() == nil {
		return png.Encode(w, img)
	}
	defer func() {
		if value := recover(); value != nil {
			if canceled, ok := value.(pngCanceled); ok {
				err = canceled.err
			} else {
				panic(value)
			}
		}
	}()
	return png.Encode(w, &interruptibleImage{RGBA: img, ctx: ctx})
}
