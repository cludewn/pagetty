// Package display は、記録側と画像生成側で共有する解釈済みの端末表示を定義する。
package display

// Document は、スクロールバックと最終画面を表示順に一度ずつ含む。画面更新の履歴ではない。
// 端末制御・属性・折り返しは記録側で解決し、画像側では再解釈しない。
//
// Columns は記録時の正の端末列数で、各行の表示幅はこれを超えない。
// Rows は未使用の画面領域を除き、出力由来の空行と属性付きの空白を保持する。
// nil・長さ0は表示内容なし、空のRow一つは空行一つを表す。
//
// 記録側は書き込みを終えてから渡し、受け取り側はスライスも含めて読み取り専用として扱う。
type Document struct {
	Columns int
	Rows    []Row
}

// Row は折り返し後の表示行で、元の改行の有無には依存しない。
// Cells は列0から隙間なく並べる。カーソル移動による空きや属性付き・消去後の空白も
// スペースのセルで表す。省略した末尾の列はStyle{}の空白とみなす。
type Row struct {
	Cells []Cell
}

// Cell は結合文字を含む一つの表示文字と、その有効な属性を保持する。
// Text は空でない有効なUTF-8で、制御文字やエスケープシーケンスを含めない。
// 空白はTextが" "、Widthが1のセルで表す。
// Width は記録側が確定した表示列数（1または2）で、画像側では再計算しない。
// 全角文字は幅2のセル一つで表し、継続セルは追加しない。
// 結合文字は基底文字のTextに含め、幅0のセルは作らない。
type Cell struct {
	Text  string
	Width int
	Style Style
}

// Style は前のセル・行からの差分ではなく、完全な表示属性を表す。ゼロ値は標準色・装飾なし。
// Inverse は標準色の解決後に前景と背景を交換する。
// Bold・Faintはパレット色の明るさとは独立して扱う。
type Style struct {
	Foreground    Color
	Background    Color
	Bold          bool
	Faint         bool
	Italic        bool
	Underline     bool
	Strikethrough bool
	Inverse       bool
}

// ColorKind は標準色・パレット色・RGBの区別を表す。
type ColorKind uint8

const (
	ColorDefault ColorKind = iota
	ColorIndexed
	ColorRGB
)

// Color は描画テーマに依存せず端末の色指定を保持する。
// ColorDefault は用途に応じて描画側の標準前景色・背景色に解決する。
// ColorIndexed はIndex（0〜15は通常・高輝度のANSI色、16〜255は拡張パレット）、
// ColorRGB はR・G・Bを使う。使わないフィールドはゼロにする。
type Color struct {
	Kind    ColorKind
	Index   uint8
	R, G, B uint8
}
