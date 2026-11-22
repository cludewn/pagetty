package render

import (
	"image"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"
)

type pngScanStep struct {
	point fixed.Point26_6
	move  bool
}

type pngScanner struct {
	*rasterx.ScannerGV
	canvas *image.RGBA
	band   image.RGBA
	steps  []pngScanStep
	extent fixed.Rectangle26_6
}

func newPNGScanner(img *image.RGBA) *pngScanner {
	s := &pngScanner{canvas: img}
	s.ScannerGV = rasterx.NewScannerGV(img.Rect.Dx(), 0, &s.band, img.Rect)
	return s
}

// ベクター描画は元のページの色・座標系を使い、走査マスクの高さだけを変える。
func (s *pngScanner) add(p fixed.Point26_6, move bool) {
	if len(s.steps) == 0 {
		s.extent = fixed.Rectangle26_6{Min: p, Max: p}
	} else {
		s.extent.Min.X = min(s.extent.Min.X, p.X)
		s.extent.Min.Y = min(s.extent.Min.Y, p.Y)
		s.extent.Max.X = max(s.extent.Max.X, p.X)
		s.extent.Max.Y = max(s.extent.Max.Y, p.Y)
	}
	s.steps = append(s.steps, pngScanStep{point: p, move: move})
}

func (s *pngScanner) Start(p fixed.Point26_6) { s.add(p, true) }
func (s *pngScanner) Line(p fixed.Point26_6)  { s.add(p, false) }

func (s *pngScanner) Clear() {
	s.steps = s.steps[:0]
	s.extent = fixed.Rectangle26_6{}
}

func (s *pngScanner) GetPathExtent() fixed.Rectangle26_6 { return s.extent }

// NewDasher は初期化時にページ全体の寸法を渡す。マスクはDrawのときだけ確保する。
func (s *pngScanner) SetBounds(_, _ int) {}

func (s *pngScanner) Draw() {
	if len(s.steps) == 0 {
		return
	}
	w, h := s.canvas.Rect.Dx(), s.canvas.Rect.Dy()
	top := max(0, s.extent.Min.Y.Floor())
	// 切り上げ時に上限付近の固定小数点がオーバーフローしないよう先に切る。
	bottom := min(s.extent.Max.Y, fixed.I(h)).Ceil()
	if top >= bottom {
		return
	}
	// x/image/vector v0.45.0 は一辺が512を超えると浮動小数点で描く。
	// 細長いページでも、切り出し前と同じアンチエイリアス計算を選ぶ。
	const fixedLimit = 512
	if w <= fixedLimit && h > fixedLimit {
		top = min(top, h-fixedLimit-1)
		bottom = max(bottom, top+fixedLimit+1)
	}
	s.band = image.RGBA{
		Pix: s.canvas.Pix[top*s.canvas.Stride:], Stride: s.canvas.Stride,
		Rect: image.Rect(0, top, w, bottom),
	}
	s.ScannerGV.Offset = image.Pt(0, top)
	s.ScannerGV.SetBounds(w, bottom-top)
	for _, step := range s.steps {
		p := step.point
		p.Y -= fixed.I(top)
		if step.move {
			s.ScannerGV.Start(p)
		} else {
			s.ScannerGV.Line(p)
		}
	}
	s.ScannerGV.Draw()
}
