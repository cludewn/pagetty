package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cludewn/pagetty/internal/capture"
	"github.com/cludewn/pagetty/internal/render"
	"golang.org/x/term"
)

const usage = `PageTTY — 対話型CLIの実行結果を画像として保存します。

使い方:
  pagetty [options] -- <command> [args...]
  pagetty --help
`

var errRedirectedInput = errors.New("PageTTY自身へのパイプ・ファイル入力は非対応です。実端末を使用するか、子シェル内で < input.txt を指定してください（pagetty --help参照）")

func runCLI(ctx context.Context, args []string, input *os.File, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("pagetty", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	format := flags.String("format", "png", "画像形式: png または svg")
	columns := flags.Int("columns", 80, "端末の表示列数 (2〜32767、画像寸法の上限あり)")
	maxLines := flags.Int("max-lines", 44, "画像1枚あたりの最大表示行数 (正の整数)")
	outputDir := flags.String("output-dir", "./captures/", "起動元を基準とした画像保存先")
	printHelp := func() {
		flags.SetOutput(output)
		fmt.Fprint(flags.Output(), usage)
		fmt.Fprintln(flags.Output(), "\nオプション:")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), `
例:
  pagetty -- ./a.out
  pagetty --format svg --output-dir ./results -- python main.py
  pagetty -- sh -c 'exec ./a.out < input.txt'
  pagetty -- cmd.exe /d /c ".\a.exe < input.txt"

コマンドと引数はシェル展開せずに渡します。
PTY行数は実端末から取得し、取得できなければ24行を使用します。
PageTTY自身へのパイプ・ファイル入力は非対応です。入力ファイルは上記の子シェル内で指定してください。
対話入力には実端末（Windowsではコンソール）が必要です。/dev/null・NULは入力なしとして扱います。
中断・記録エラー時はpartial付きの名前で部分記録の保存を試みます。
終了コード: 対象のコードを引継ぎ、引数不正2、ツール側失敗1、中断130。
画像生成・保存の失敗は中断や対象のコードより優先します。`)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printHelp()
		return 0
	}
	flags.Usage = func() { fmt.Fprint(diagnostics, usage) }
	separator := slices.Index(args, "--")
	if separator < 0 || separator+1 >= len(args) || args[separator+1] == "" {
		fmt.Fprintln(diagnostics, "pagetty: 引数エラー: -- の後に実行コマンドを指定してください")
		return 2
	}
	if err := flags.Parse(args[:separator]); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(diagnostics, "pagetty: 引数エラー: -- の前にはオプションだけを指定してください")
		return 2
	}
	if (*format != "png" && *format != "svg") || *columns < 2 || *columns > 32767 || *maxLines < 1 || *outputDir == "" {
		fmt.Fprintln(diagnostics, "pagetty: 引数エラー: formatはpng/svg、columnsは2〜32767、max-linesは正の整数、output-dirは空でないパスを指定してください")
		return 2
	}
	input, err := cliInput(input)
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: 標準入力エラー: %v\n", err)
		return 2
	}
	dir, err := filepath.Abs(*outputDir)
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: 保存先の解決失敗: %v\n", err)
		return 1
	}
	started := time.Now()
	fmt.Fprintln(diagnostics, "pagetty: Running command...")
	rows := terminalRows(input, output)
	result, captureErr := capture.Run(ctx, args[separator+1:], capture.Options{
		Columns: *columns, Rows: rows, Input: input, Output: output,
	})
	captureTime := time.Since(started)
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
	partial := captureErr != nil || result.Interrupted
	if captureErr != nil && !result.Interrupted {
		code = 1
	}
	if len(result.Document.Rows) == 0 {
		fmt.Fprintln(diagnostics, "pagetty: No visible output to save.")
		return code
	}
	renderStart := time.Now()
	update, stop := startRenderProgress(diagnostics, isTerminal(diagnostics), renderStart)
	images, err := render.Render(result.Document, render.Options{
		Format: render.Format(*format), MaxLines: *maxLines, Progress: update,
	})
	renderTime := time.Since(renderStart)
	stop()
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: レンダリング失敗: %v\n", err)
		return 1
	}
	fmt.Fprintf(diagnostics, "pagetty: Saving %d %s...\n", len(images), pageWord(len(images)))
	saveStart := time.Now()
	paths, err := saveImages(dir, started.Format("20060102-150405"), partial, images)
	saveTime := time.Since(saveStart)
	for _, path := range paths {
		fmt.Fprintf(diagnostics, "pagetty: Saved to %s\n", path)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: 画像保存失敗（%d/%dページ保存）: %v\n", len(paths), len(images), err)
		return 1
	}
	status := ""
	if partial {
		status = "partial capture; "
	} else if code != 0 {
		status = "command failed; "
	}
	fmt.Fprintf(diagnostics, "pagetty: Saved %d %s %s (%stotal %s, capture %s, render %s, save %s)\n",
		len(paths), strings.ToUpper(*format), pageWord(len(paths)), status, time.Since(started).Round(time.Millisecond),
		captureTime.Round(time.Millisecond), renderTime.Round(time.Millisecond), saveTime.Round(time.Millisecond))
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
