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

func runCLI(ctx context.Context, args []string, input *os.File, output, stderr io.Writer) int {
	width := renderTerminalWidth(stderr)
	diagnostics := newDiagnosticWriter(stderr)
	defer diagnostics.close()
	flags := flag.NewFlagSet("pagetty", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	format := flags.String("format", "png", "画像形式: png または svg")
	columns := flags.Int("columns", 80, "端末の表示列数 (2〜3273、端末セル数の上限あり)")
	maxLines := flags.Int("max-lines", 44, "画像1枚あたりの最大表示行数 (正の整数)")
	pageLimit := flags.Int("page-limit", render.DefaultPageLimit, "画像の保存範囲を確認する枚数 (1〜64)")
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
端末は65,536セルまでです。記録は2 MiB、4,096表示行、列数×保持行数131,072セルまでです。
画像1枚は最大辺32,768 px・16 Miピクセルまでで、寸法に収まる行数で分割します。
画像は最大64枚です。PNGの描画は合計32 Miピクセル、内部SVGは1枚16 MiB、最終画像は1枚8 MiB・合計64 MiBまでです。
対話環境では画像が--page-limitを超えると、生成前に保存範囲を確認します。
a は保存可能な全ページ、l は設定上限までを保存します。n・Enter・EOFは保存しません。
非対話環境では設定上限まで保存します。選択によって安全上の上限は変わりません。
記録量の上限を超えると記録だけを打ち切り、対象との入出力は継続します。
端末制御で履歴の先頭が上書きされた場合は、欠落を通知します。
実際に省略が生じた場合や記録エラー時は、partial付きの部分記録の保存を試みます。
Ctrl+Cは対象へ転送します。対象が割り込みを捕捉・無視して実行を続ける場合は、PageTTYも実行を続けます。
Ctrl+Cによる中断では、対象終了後の確認・生成・保存中も今回の画像を破棄します。
終了コード: 通常は対象のコード、資源制限・非対話時の打ち切り・ツール側失敗1、引数不正2、中断130。
利用者が保存を辞退したり、設定上限までを選んだりしただけでは終了コードは変わりません。`)
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
	if (*format != "png" && *format != "svg") || *columns < 2 || *columns > capture.MaxColumns || *maxLines < 1 || *pageLimit < 1 || *pageLimit > render.MaxPages || *outputDir == "" {
		fmt.Fprintln(diagnostics, "pagetty: 引数エラー: formatはpng/svg、columnsは2〜3273、max-linesは正の整数、page-limitは1〜64、output-dirは空でないパスを指定してください")
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
	rows := terminalRows(input, output)
	_, err = render.PageLines(*columns, *maxLines)
	if err == nil {
		err = capture.ValidateSize(*columns, rows)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "pagetty: 引数エラー: %v\n", err)
		return 2
	}
	started := time.Now()
	fmt.Fprintln(diagnostics, "pagetty: Running command...")
	var limitNotice <-chan diagnosticResult
	result, captureErr := capture.Run(ctx, args[separator+1:], capture.Options{
		Columns: *columns, Rows: rows, Input: input, Output: output,
		OnRecordLimit: func(reason string, running bool) {
			if running {
				limitNotice = diagnostics.notice(fmt.Sprintf("\npagetty: 記録を打ち切りました（%s）。対象プログラムの実行と入出力は継続します。\n", reason))
			}
		},
	})
	// 対象の最終行に改行がなくても、案内を実行結果から分離する。
	fmt.Fprintln(diagnostics)
	if err := withoutCancellation(captureErr); err != nil {
		fmt.Fprintf(diagnostics, "pagetty: PTY実行・記録失敗: %v\n", err)
	}
	if result.Interrupted || ctx.Err() != nil {
		return interrupted(diagnostics)
	}
	if !result.Started {
		return 1
	}
	code := result.ExitCode
	switch {
	case code < 0:
		fmt.Fprintln(diagnostics, "pagetty: 対象プログラムの終了失敗: 終了コードを取得できません（シグナル終了など）")
		code = 1
	case code != 0:
		fmt.Fprintf(diagnostics, "pagetty: 対象プログラムの終了失敗: 終了コード %d\n", code)
	}
	partial := captureErr != nil || result.Limit != ""
	if partial {
		code = 1
	}
	if result.Limit != "" {
		// 改行の書き込みを待ったので、先行する通知の送信結果を確認できる。
		var notified bool
		select {
		case receipt := <-limitNotice:
			notified = receipt.err == nil
		default:
		}
		if notified {
			fmt.Fprintf(diagnostics, "pagetty: 対象の終了コード %d\n", result.ExitCode)
		} else {
			fmt.Fprintf(diagnostics, "pagetty: 記録を打ち切りました（%s、対象の終了コード %d）\n", result.Limit, result.ExitCode)
		}
	}
	if len(result.Document.Rows) == 0 {
		if ctx.Err() != nil {
			return interrupted(diagnostics)
		}
		fmt.Fprintln(diagnostics, "pagetty: No visible output to save.")
		return code
	}

	save := imageSave{dir: dir}
	var update func(int, int)
	var stop func()
	options := render.Options{
		Format: render.Format(*format), MaxLines: *maxLines, PageLimit: *pageLimit,
		Progress: func(done, total int) {
			if update == nil {
				update, stop = startRenderProgress(diagnostics, width, time.Now())
			}
			update(done, total)
		},
	}
	if input != nil && width != nil {
		options.ChoosePages = func(total, available int, hardLimit render.Limit) (int, error) {
			count, err := confirmPages(ctx, input, diagnostics, total, *pageLimit, available, hardLimit, partial)
			if err != nil {
				return 0, fmt.Errorf("保存範囲の確認失敗: %w", err)
			}
			partial = partial || (count > 0 && count < total)
			return count, nil
		}
	}
	var saveErr error
	limit, renderErr := render.RenderPages(ctx, result.Document, options, func(img render.Image) error {
		saveErr = save.add(ctx, img)
		return saveErr
	})
	if stop != nil {
		stop()
	}
	if saveErr != nil {
		renderErr = nil
	}
	if limit != "" {
		partial, code = true, 1
		fmt.Fprintf(diagnostics, "pagetty: 画像生成を打ち切りました（%s、対象の終了コード %d）\n", limit, result.ExitCode)
	}
	if ctx.Err() == nil && renderErr == nil && saveErr == nil && len(save.staged) > 0 {
		fmt.Fprintf(diagnostics, "pagetty: Saving %d %s...\n", len(save.staged), pageWord(len(save.staged)))
		saveErr = save.publish(ctx, started.Format("20060102-150405"), partial)
	}
	// 公開後の確認を確定点とする。この時点までの中断は今回の公開を取り消す。
	canceled := ctx.Err() != nil || errors.Is(renderErr, context.Canceled)
	cleanupErr := save.close(canceled || saveErr != nil)
	if renderErr = withoutCancellation(renderErr); renderErr != nil {
		fmt.Fprintf(diagnostics, "pagetty: レンダリング失敗: %v\n", renderErr)
	}
	if saveErr = withoutCancellation(saveErr); saveErr != nil {
		fmt.Fprintf(diagnostics, "pagetty: 画像保存失敗: %v\n", saveErr)
	}
	if cleanupErr != nil {
		fmt.Fprintf(diagnostics, "pagetty: 保存の後処理に失敗しました: %v\n", cleanupErr)
	}
	if saveErr != nil {
		return 1
	}
	if canceled {
		if cleanupErr != nil {
			fmt.Fprintln(diagnostics, "pagetty: 中断されました。画像の後処理結果を確認してください。")
			return 130
		}
		return interrupted(diagnostics)
	}
	for _, path := range save.published {
		fmt.Fprintf(diagnostics, "pagetty: Output: %s\n", path)
	}
	if renderErr != nil || cleanupErr != nil {
		return 1
	}
	if len(save.published) == 0 {
		return code
	}
	label := "Saved"
	if partial {
		label = "Partial capture:"
	} else if code != 0 {
		label = "Command failed; saved"
	}
	fmt.Fprintf(diagnostics, "pagetty: %s %d %s %s in %s\n",
		label, len(save.published), strings.ToUpper(*format), pageWord(len(save.published)), completionDuration(time.Since(result.CommandEndedAt)))
	return code
}

func interrupted(w io.Writer) int {
	fmt.Fprintln(w, "pagetty: 中断されました。今回の画像は保存しません。")
	return 130
}

func withoutCancellation(err error) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result error
		for _, part := range joined.Unwrap() {
			result = errors.Join(result, withoutCancellation(part))
		}
		return result
	}
	var writeErr *capture.WriteError
	if errors.As(err, &writeErr) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return withoutCancellation(errors.Unwrap(err))
	}
	return err
}

func completionDuration(elapsed time.Duration) string {
	if elapsed < time.Millisecond {
		return "<1ms"
	}
	if elapsed < time.Second {
		return elapsed.Round(time.Millisecond).String()
	}
	return elapsed.Round(10 * time.Millisecond).String()
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
