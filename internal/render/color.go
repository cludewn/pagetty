package render

import (
	"image/color"

	"github.com/cludewn/pagetty/internal/display"
)

var (
	defaultForeground = color.RGBA{220, 220, 220, 255}
	defaultBackground = color.RGBA{23, 23, 23, 255}
)

func colors(style display.Style) (color.RGBA, color.RGBA) {
	fg := resolveColor(style.Foreground, defaultForeground)
	bg := resolveColor(style.Background, defaultBackground)
	if style.Inverse {
		fg, bg = bg, fg
	}
	if style.Faint {
		fg.R = uint8((uint16(fg.R) + uint16(bg.R)) / 2)
		fg.G = uint8((uint16(fg.G) + uint16(bg.G)) / 2)
		fg.B = uint8((uint16(fg.B) + uint16(bg.B)) / 2)
	}
	return fg, bg
}

func resolveColor(c display.Color, fallback color.RGBA) color.RGBA {
	if c.Kind == display.ColorRGB {
		return color.RGBA{c.R, c.G, c.B, 255}
	}
	if c.Kind != display.ColorIndexed {
		return fallback
	}
	index := int(c.Index)
	if index < 16 {
		return ansiPalette[index]
	}
	if index >= 232 {
		v := uint8(8 + (index-232)*10)
		return color.RGBA{v, v, v, 255}
	}
	index -= 16
	levels := [...]uint8{0, 95, 135, 175, 215, 255}
	return color.RGBA{levels[index/36], levels[(index/6)%6], levels[index%6], 255}
}

var ansiPalette = [...]color.RGBA{
	{0, 0, 0, 255}, {128, 0, 0, 255}, {0, 128, 0, 255}, {128, 128, 0, 255},
	{0, 0, 128, 255}, {128, 0, 128, 255}, {0, 128, 128, 255}, {192, 192, 192, 255},
	{128, 128, 128, 255}, {255, 0, 0, 255}, {0, 255, 0, 255}, {255, 255, 0, 255},
	{0, 0, 255, 255}, {255, 0, 255, 255}, {0, 255, 255, 255}, {255, 255, 255, 255},
}
