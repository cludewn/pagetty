package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/cludewn/pagetty/internal/capture"
	"github.com/cludewn/pagetty/internal/render"
	"golang.org/x/term"
)

const usage = `PageTTY — 対話型CLIの実行結果を画像として保存します。

使い方:
  pagetty capture [options] -- <command> [args...]
  pagetty capture --help
`

func runCLI(ctx context.Context, args []string, input *os.File, output, diagnostics io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(output, usage)
		return 0
	}
	if len(args) == 0 || args[0] != "capture" {
		fmt.Fprint(diagnostics, usage)
		return 2
	}
	args = args[1:]
	flags := flag.NewFlagSet("capture", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	format := flags.String("format", "png", "画像形式: png または svg")
	columns := flags.Int("columns", 100, "端末の表示列数 (2〜32767、画像寸法の上限あり)")
	maxLines := flags.Int("max-lines", 40, "画像1枚あたりの最大表示行数 (正の整数)")
	outputDir := flags.String("output-dir", "./captures/", "起動元を基準とした画像保存先")
	flags.Usage = func() {
		flags.SetOutput(output)
		fmt.Fprint(flags.Output(), usage)
		fmt.Fprintln(flags.Output(), "\nオプション:")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), `
例:
  pagetty capture -- ./a.out
  pagetty capture --format svg --output-dir ./results -- python main.py

コマンドと引数はシェル展開せずに渡します。
PTY行数は実端末から取得し、取得できなければ24行を使用します。
Unixのパイプ・ファイル入力はcanonical modeのPTYへEOFを通知します。
raw mode・EOF文字の無効化・転送中の端末モード変更は保証しません。
Windowsの対話入力にはコンソールが必要です。リダイレクト入力は転送しません。
中断・記録エラー時はpartial付きの名前で部分記録の保存を試みます。
終了コード: 対象のコードを引継ぎ、引数不正2、ツール側失敗1、中断130。
画像生成・保存の失敗は中断や対象のコードより優先します。`)
	}
	separator := slices.Index(args, "--")
	optionArgs := args
	if separator >= 0 {
		optionArgs = args[:separator]
	}
	if err := flags.Parse(optionArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if separator < 0 || flags.NArg() != 0 || separator+1 >= len(args) || args[separator+1] == "" {
		fmt.Fprintln(diagnostics, "pagetty: 引数エラー: -- の後に実行コマンドを指定してください")
		return 2
	}
	if (*format != "png" && *format != "svg") || *columns < 2 || *columns > 32767 || *maxLines < 1 || *outputDir == "" {
		fmt.Fprintln(diagnostics, "pagetty: 引数エラー: formatはpng/svg、columnsは2〜32767、max-linesは正の整数、output-dirは空でないパスを指定してください")
		return 2
	}
	dir, err := filepath.Abs(*outputDir)
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: 保存先の解決失敗: %v\n", err)
		return 1
	}
	prefix := "capture-" + time.Now().Format("20060102-150405.000000000-0700") + "-" + rand.Text()
	rows := terminalRows(input, output)
	if input != nil && !term.IsTerminal(int(input.Fd())) {
		info, statErr := input.Stat()
		// /dev/nullは入力待ちに登録できない。Windowsはコンソール入力だけを転送する。
		if runtime.GOOS == "windows" || (statErr == nil && info.Mode()&os.ModeCharDevice != 0) {
			input = nil
		}
	}
	result, captureErr := capture.Run(ctx, args[separator+1:], capture.Options{
		Columns: *columns, Rows: rows, Input: input, Output: output,
	})
	// 対象の最終行に改行がなくても、案内を実行結果から分離する。
	fmt.Fprintln(diagnostics)
	if captureErr != nil {
		fmt.Fprintf(diagnostics, "pagetty: PTY実行・記録失敗: %v\n", captureErr)
	}
	if !result.Started {
		return 1
	}
	code := result.ExitCode
	if result.Interrupted {
		fmt.Fprintln(diagnostics, "pagetty: 対象プログラムは中断されました（部分記録）")
		code = 130
	} else if code < 0 {
		fmt.Fprintln(diagnostics, "pagetty: 対象プログラムの終了失敗: 終了コードを取得できません（シグナル終了など）")
		code = 1
	} else if code != 0 {
		fmt.Fprintf(diagnostics, "pagetty: 対象プログラムの終了失敗: 終了コード %d\n", code)
	}
	if captureErr != nil || result.Interrupted {
		prefix += "-partial"
		if !result.Interrupted {
			code = 1
		}
	}
	if len(result.Document.Rows) == 0 {
		fmt.Fprintln(diagnostics, "pagetty: 記録された表示がないため画像を保存しませんでした")
		return code
	}
	images, err := render.Render(result.Document, render.Options{Format: render.Format(*format), MaxLines: *maxLines})
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: レンダリング失敗: %v\n", err)
		return 1
	}
	paths, err := saveImages(dir, prefix, images)
	for _, path := range paths {
		fmt.Fprintf(diagnostics, "pagetty: 保存: %s\n", path)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: 画像保存失敗（%d/%dページ保存）: %v\n", len(paths), len(images), err)
		return 1
	}
	fmt.Fprintf(diagnostics, "pagetty: %dページの%s画像を保存しました\n", len(paths), *format)
	return code
}

func terminalRows(input *os.File, output io.Writer) int {
	var files []interface{ Fd() uintptr }
	if input != nil {
		files = append(files, input)
	}
	if f, ok := output.(interface{ Fd() uintptr }); ok {
		files = append(files, f)
	}
	for _, f := range files {
		if _, rows, err := term.GetSize(int(f.Fd())); err == nil && rows >= 1 && rows <= 32767 {
			return rows
		}
	}
	return 24
}
