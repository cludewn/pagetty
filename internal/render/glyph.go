package render

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/harfbuzz"
)

type loadedFont struct {
	face   *font.Face
	shaper *harfbuzz.Font
}

type glyphPath struct {
	data    string
	advance float64
}

func loadFont(data []byte) (*loadedFont, error) {
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return &loadedFont{face, harfbuzz.NewFont(face)}, nil
}

func (f *loadedFont) path(text string) (glyphPath, error) {
	buffer := harfbuzz.NewBuffer()
	buffer.AddRunes([]rune(text), 0, -1)
	buffer.GuessSegmentProperties()
	buffer.Shape(f.shaper, nil)
	scale := float64(fontSize) / float64(f.face.Upem())
	var data strings.Builder
	x, y := int32(0), int32(0)
	for i, info := range buffer.Info {
		if info.Glyph == 0 {
			// 未収録のクラスタだけを置換し、セル幅と属性は描画側に任せる。
			if text != "□" {
				return f.path("□")
			}
			return glyphPath{}, fmt.Errorf("font has no glyph for this text")
		}
		outline, ok := f.face.GlyphData(info.Glyph).(font.GlyphOutline)
		if !ok {
			return glyphPath{}, fmt.Errorf("font has no outline for this glyph")
		}
		position := buffer.Pos[i]
		open := false
		for _, segment := range outline.Segments {
			count := 1
			switch segment.Op {
			case opentype.SegmentOpMoveTo:
				if open {
					data.WriteByte('Z')
				}
				open = true
				data.WriteByte('M')
			case opentype.SegmentOpLineTo:
				data.WriteByte('L')
			case opentype.SegmentOpQuadTo:
				data.WriteByte('Q')
				count = 2
			case opentype.SegmentOpCubeTo:
				data.WriteByte('C')
				count = 3
			default:
				return glyphPath{}, fmt.Errorf("unsupported font outline segment")
			}
			for _, point := range segment.Args[:count] {
				// フォント座標は上向き、SVG座標は下向き。
				px := (float64(point.X) + float64(x+position.XOffset)) * scale
				py := -(float64(point.Y) + float64(y+position.YOffset)) * scale
				fmt.Fprintf(&data, "%g %g ", px, py)
			}
		}
		if open {
			data.WriteByte('Z')
		}
		x += position.XAdvance
		y += position.YAdvance
	}
	return glyphPath{data.String(), float64(x) * scale}, nil
}
