package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

const cliTestCommandWait = 150 * time.Millisecond

func childArgs(mode string, args ...string) []string {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return append([]string{exe, "-test.run=^TestCLIChild$", "--", "pagetty-cli-child", mode}, args...)
}

func TestCLIChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "pagetty-cli-child" || i+1 >= len(os.Args) {
			continue
		}
		switch os.Args[i+1] {
		case "mark":
			if os.WriteFile(os.Args[i+2], []byte("started"), 0600) != nil {
				os.Exit(11)
			}
			fmt.Print("started")
		case "file-input":
			data, err := io.ReadAll(os.Stdin)
			if err != nil || term.IsTerminal(int(os.Stdin.Fd())) {
				os.Exit(12)
			}
			fmt.Printf("INPUT:%q\ntty:%t:%t\n", data, term.IsTerminal(int(os.Stdout.Fd())), term.IsTerminal(int(os.Stderr.Fd())))
		case "text":
			fmt.Print("日本語ABCDEF")
		case "args":
			data, _ := json.Marshal(os.Args[i+2:])
			fmt.Print(string(data))
		case "lines", "delayed-lines":
			if os.Args[i+1] == "delayed-lines" {
				time.Sleep(cliTestCommandWait)
			}
			for i := range 5 {
				fmt.Printf("line%d\n", i)
			}
		case "missing-glyph":
			fmt.Print("A日éＡ\x1b[31;1m🚀\x1b[0mZ")
		case "count":
			count, _ := strconv.Atoi(os.Args[i+2])
			for n := range count {
				fmt.Printf("line%d\n", n)
			}
			if i+3 < len(os.Args) {
				code, _ := strconv.Atoi(os.Args[i+3])
				os.Exit(code)
			}
		case "limit-input", "limit-interrupt":
			fmt.Print("first\nsecond\n" + strings.Repeat("more\n", 1800) + "post-limit>")
			if os.Args[i+1] == "limit-interrupt" {
				time.Sleep(time.Minute)
			}
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil {
				os.Exit(9)
			}
			fmt.Printf("received:%s", line)
		case "ignore-repeat":
			if err := ignoreTestInterrupt(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(15)
			}
			fmt.Print("ready")
			time.Sleep(200 * time.Millisecond)
			fmt.Print("\nfirst interrupt ignored")
			time.Sleep(200 * time.Millisecond)
			fmt.Print("\nstill running>")
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "continue" {
				os.Exit(16)
			}
			fmt.Print("continued after interrupts")
		case "raw-key":
			old, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				os.Exit(13)
			}
			fmt.Print("ready")
			var key [1]byte
			_, err = io.ReadFull(os.Stdin, key[:])
			_ = term.Restore(int(os.Stdin.Fd()), old)
			if err != nil || key[0] != 3 {
				os.Exit(14)
			}
			fmt.Print("raw key received")
		case "catch-exit", "catch-twice":
			intr := make(chan os.Signal, 1)
			signal.Notify(intr, os.Interrupt)
			fmt.Print("ready")
			<-intr
			fmt.Print("\nfirst interrupt handled")
			if os.Args[i+1] != "catch-exit" {
				<-intr
				fmt.Print("\nsecond interrupt handled")
			}
			os.Exit(7)
		case "updated-only":
			fmt.Print("NEW")
		case "cursor-update":
			fmt.Print("old\x1b[4;1H")
			time.Sleep(cliTestCommandWait)
			fmt.Print("\x1b[1;1H\rNEW")
		case "notice-tail":
			fmt.Print(strings.Repeat("line\n", 1500) + "OUTPUT-TAIL")
			time.Sleep(cliTestCommandWait)
		case "long":
			for i := range 81 {
				fmt.Printf("line%d\n", i)
			}
		case "nonzero":
			fmt.Print("before failure")
			os.Exit(7)
		case "block":
			fmt.Print("ready")
			time.Sleep(time.Minute)
		case "interactive":
			r := bufio.NewReader(os.Stdin)
			for i := range 2 {
				fmt.Printf("input%d:", i)
				line, err := r.ReadString('\n')
				if err != nil {
					os.Exit(9)
				}
				fmt.Printf("got:%s", line)
			}
		case "size":
			cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
			fmt.Printf("size:%d:%d:%v", cols, rows, err)
		case "empty":
		default:
			os.Exit(10)
		}
		os.Exit(0)
	}
}

func cliArgs(options []string, mode string, args ...string) []string {
	result := append([]string(nil), options...)
	result = append(result, "--")
	return append(result, childArgs(mode, args...)...)
}

func runTestCLI(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var output, diagnostics bytes.Buffer
	code := runCLI(ctx, args, nil, &output, &diagnostics)
	return code, output.String(), diagnostics.String()
}

func TestCLIHelpAndInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		code, output, diagnostics := runTestCLI(t, args)
		if code != 0 || strings.Contains(output, "pagetty capture") || !strings.Contains(output, "pagetty [options] -- <command> [args...]") || !strings.Contains(output, "--format svg") ||
			!strings.Contains(output, "page-limit") || strings.Contains(output, "max-pages") ||
			!strings.Contains(output, "a は保存可能な全ページ、l は設定上限までを保存します。n・Enter・EOFは保存しません。") || diagnostics != "" {
			t.Fatalf("args=%q code=%d output=%q diagnostics=%q", args, code, output, diagnostics)
		}
	}
	for _, args := range [][]string{
		nil, {"echo", "text"}, {"--format", "svg", "echo"}, {"--help", "extra"},
		{"--"}, {"--format", "svg", "--"}, {"--", ""}, {"unexpected", "--", "echo"},
		{"capture", "--help"}, {"capture", "--", "echo"}, {"--help", "--", "echo"},
		{"--unknown", "--", "echo"}, {"--max-pages", "16", "--", "echo"}, {"--columns", "--", "echo"}, {"--columns", "x", "--", "echo"},
	} {
		code, _, diagnostics := runTestCLI(t, args)
		if code != 2 || diagnostics == "" {
			t.Fatalf("args=%q code=%d diagnostics=%q", args, code, diagnostics)
		}
	}
	for _, options := range [][]string{
		{"--format", "jpeg"}, {"--columns", "1"}, {"--columns", "32768"},
		{"--page-limit", "0"}, {"--page-limit", "65"}, {"--max-lines", "0"}, {"--max-lines", "-1"}, {"--output-dir", ""},
	} {
		code, output, diagnostics := runTestCLI(t, cliArgs(options, "text"))
		if code != 2 || output != "" || !strings.Contains(diagnostics, "引数エラー") {
			t.Fatalf("options=%q code=%d output=%q diagnostics=%q", options, code, output, diagnostics)
		}
	}
}

func imageFiles(t *testing.T, dir, extension string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*."+extension))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func readImage(t *testing.T, path string) (int, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) == ".png" {
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return config.Width, config.Height
	}
	var svg struct {
		XMLName xml.Name `xml:"svg"`
		Width   string   `xml:"width,attr"`
		Height  string   `xml:"height,attr"`
	}
	if err := xml.Unmarshal(data, &svg); err != nil {
		t.Fatal(err)
	}
	w, wErr := strconv.Atoi(strings.TrimSuffix(svg.Width, "px"))
	h, hErr := strconv.Atoi(strings.TrimSuffix(svg.Height, "px"))
	if err := errors.Join(wErr, hErr); err != nil {
		t.Fatal(err)
	}
	return w, h
}

func TestCLIFormatsWidthAndPages(t *testing.T) {
	for _, format := range []string{"png", "svg"} {
		t.Run(format, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "nested", "images")
			code, output, diagnostics := runTestCLI(t, cliArgs([]string{
				"--format", format, "--columns", "8", "--max-lines", "1", "--output-dir", dir,
			}, "text"))
			if code != 0 || !strings.Contains(output, "日本語ABCDEF") {
				t.Fatalf("code=%d output=%q diagnostics=%q", code, output, diagnostics)
			}
			paths := imageFiles(t, dir, format)
			if len(paths) != 2 || !strings.Contains(diagnostics, "Saved 2 "+strings.ToUpper(format)+" pages") {
				t.Fatalf("paths=%v diagnostics=%q", paths, diagnostics)
			}
			prefix := strings.TrimSuffix(paths[0], "_001."+format)
			if paths[1] != prefix+"_002."+format {
				t.Fatalf("pages do not share a run prefix: %v", paths)
			}
			for _, path := range paths {
				w, h := readImage(t, path)
				if w != 112 || h != 60 || !strings.Contains(diagnostics, path) {
					t.Fatalf("path=%s size=%dx%d diagnostics=%q", path, w, h, diagnostics)
				}
			}
		})
	}

	dir := t.TempDir()
	code, _, diagnostics := runTestCLI(t, cliArgs([]string{"--output-dir", dir, "--max-lines", "2"}, "lines"))
	paths := imageFiles(t, dir, "png")
	if code != 0 || len(paths) != 3 {
		t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics)
	}
	for i, path := range paths {
		w, h := readImage(t, path)
		wantHeight := 88
		if i == 2 {
			wantHeight = 60
		}
		if w != 832 || h != wantHeight {
			t.Fatalf("page=%d size=%dx%d", i, w, h)
		}
	}
}

type completionTestLog struct {
	bytes.Buffer
	running, rendering, saving, output time.Time
}

func (log *completionTestLog) Write(p []byte) (int, error) {
	switch {
	case bytes.HasPrefix(p, []byte("pagetty: Running command...")):
		log.running = time.Now()
	case log.rendering.IsZero() && bytes.HasPrefix(p, []byte("pagetty: Rendering (")):
		log.rendering = time.Now()
	case bytes.HasPrefix(p, []byte("pagetty: Saving ")):
		// 保存直前の待ちも計測区間に含まれるよう、ここで観測点を分ける。
		time.Sleep(25 * time.Millisecond)
		log.saving = time.Now()
	case log.output.IsZero() && bytes.HasPrefix(p, []byte("pagetty: Output: ")):
		log.output = time.Now()
	}
	return log.Buffer.Write(p)
}

func progressChildOutput(output string) string {
	// ConPTYは実行ファイルの出力の前後や途中に端末制御シーケンスを挿入する。
	return strings.ReplaceAll(ansi.Strip(output), "\r\n", "\n")
}

const progressChildLines = "line0\nline1\nline2\nline3\nline4\n"

func TestProgressChildOutputWithConPTYControls(t *testing.T) {
	const raw = "\x1b[?9001h\x1b[?1004h\x1b[?25l\x1b[2J\x1b[m\x1b[Hline0\r\n" +
		"\x1b]0;C:\\Temp\\pagetty.test.exe\a\x1b[?25h\x1b[?9001l\x1b[?1004lline1\r\nline2\r\nline3\r\nline4\r\n"
	if got := progressChildOutput(raw); got != progressChildLines {
		t.Fatalf("normalized ConPTY output = %q, want %q", got, progressChildLines)
	}
}

func TestCLIReportsPageProgressOnStderr(t *testing.T) {
	for _, format := range []string{"png", "svg"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var output bytes.Buffer
			var log completionTestLog
			args := cliArgs([]string{"--format", format, "--max-lines", "2", "--output-dir", dir}, "delayed-lines")
			code := runCLI(ctx, args, nil, &output, &log)
			if code != 0 || progressChildOutput(output.String()) != progressChildLines || strings.Contains(output.String(), "pagetty:") {
				t.Fatalf("code=%d output=%q diagnostics=%q", code, output.String(), log.String())
			}
			if strings.ContainsAny(log.String(), "\r\x1b") {
				t.Fatalf("redirected stderr contains terminal controls: %q", log.String())
			}
			diagnostics := log.String()
			for _, step := range []string{"Running command...", "Rendering 0/3 pages", "Rendering 1/3 pages",
				"Rendering 2/3 pages", "Rendering 3/3 pages", "Saving 3 pages...",
				"pagetty: Saved 3 " + strings.ToUpper(format) + " pages in "} {
				_, rest, ok := strings.Cut(diagnostics, step)
				if !ok {
					t.Fatalf("missing or out-of-order %q: %q", step, diagnostics)
				}
				diagnostics = rest
			}
			if log.running.IsZero() || log.rendering.IsZero() || log.saving.IsZero() || log.output.IsZero() || len(imageFiles(t, dir, format)) != 3 {
				t.Fatalf("missing timing checkpoint or saved pages: %s", log.String())
			}
			elapsed, err := time.ParseDuration(strings.TrimSpace(diagnostics))
			if err != nil {
				t.Fatalf("expected a single completion duration: %q", diagnostics)
			}
			// 絶対時間でなく、同じ実行で観測した区間と比べる。
			const rounding = 5 * time.Millisecond
			if excluded := log.output.Sub(log.running) - elapsed; excluded+rounding < cliTestCommandWait {
				t.Errorf("command wait was included: displayed %s, total %s", elapsed, log.output.Sub(log.running))
			}
			if minimum := log.saving.Sub(log.rendering); elapsed+rounding < minimum {
				t.Errorf("rendering or saving phase was excluded: displayed %s, minimum %s", elapsed, minimum)
			}
		})
	}
}

func TestCompletionDuration(t *testing.T) {
	for _, tt := range []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "<1ms"},
		{900 * time.Microsecond, "<1ms"},
		{time.Millisecond, "1ms"},
		{419600 * time.Microsecond, "420ms"},
		{999499 * time.Microsecond, "999ms"},
		{999500 * time.Microsecond, "1s"},
		{time.Second, "1s"},
		{1005 * time.Millisecond, "1.01s"},
		{1778 * time.Millisecond, "1.78s"},
		{59999 * time.Millisecond, "1m0s"},
		{time.Minute + 1234*time.Millisecond, "1m1.23s"},
	} {
		if got := completionDuration(tt.elapsed); got != tt.want {
			t.Errorf("duration %s: got %q, want %q", tt.elapsed, got, tt.want)
		}
	}
}

func TestCLIArgumentsAndTerminalSize(t *testing.T) {
	dir := t.TempDir()
	want := []string{"with space", "", "; echo bad", "$(echo bad)", "日本語", `a"b`, `end\`, "--help", "--format", "--"}
	code, output, diagnostics := runTestCLI(t, cliArgs([]string{"--output-dir", dir, "--columns", "256"}, "args", want...))
	start := strings.Index(output, `["`)
	var got []string
	if code != 0 || start < 0 || json.NewDecoder(strings.NewReader(output[start:])).Decode(&got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("code=%d output=%q diagnostics=%q", code, output, diagnostics)
	}
	code, output, diagnostics = runTestCLI(t, cliArgs([]string{"--output-dir", dir, "--columns", "80"}, "size"))
	if code != 0 || !strings.Contains(output, "size:80:24:<nil>") {
		t.Fatalf("code=%d output=%q diagnostics=%q", code, output, diagnostics)
	}
}

func TestCLIDefaultDirectoryAndEmptyOutput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0},
		{cliArgs(nil, "empty"), 0},
		{[]string{"--", filepath.Join(dir, "missing-command")}, 1},
	} {
		code, _, diagnostics := runTestCLI(t, tc.args)
		if code != tc.code {
			t.Fatalf("code=%d diagnostics=%q", code, diagnostics)
		}
		if tc.code == 1 && !strings.Contains(diagnostics, "PTY実行・記録失敗") {
			t.Fatalf("start failure not reported: %q", diagnostics)
		}
		if _, err := os.Stat("captures"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unused output directory exists: %v", err)
		}
	}
	code, _, diagnostics := runTestCLI(t, cliArgs(nil, "text"))
	paths := imageFiles(t, filepath.Join(dir, "captures"), "png")
	if code != 0 || len(paths) != 1 || !strings.Contains(diagnostics, paths[0]) {
		t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics)
	}
	w, h := readImage(t, paths[0])
	if w != 832 || h != 60 {
		t.Fatalf("default size=%dx%d", w, h)
	}
	longDir := filepath.Join(dir, "long")
	code, _, diagnostics = runTestCLI(t, cliArgs([]string{"--output-dir", longDir}, "long"))
	paths = imageFiles(t, longDir, "png")
	if code != 0 || len(paths) != 2 {
		t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics)
	}
	for i, path := range paths {
		_, h := readImage(t, path)
		want := 32 + 44*28
		if i == 1 {
			want = 32 + 37*28
		}
		if h != want {
			t.Fatalf("default max-lines: page=%d height=%d want=%d", i, h, want)
		}
	}
}

func TestCLINonzeroAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, mode, message string
		options             []string
		code, pages         int
	}{
		{"child failure", "nonzero", "対象プログラムの終了失敗: 終了コード 7", nil, 7, 1},
		{"invalid dimensions", "nonzero", "引数エラー", []string{"--columns", "32767"}, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "images")
			options := append([]string{"--output-dir", dir}, tc.options...)
			code, _, diagnostics := runTestCLI(t, cliArgs(options, tc.mode))
			if code != tc.code || !strings.Contains(diagnostics, tc.message) || len(imageFiles(t, dir, "png")) != tc.pages {
				t.Fatalf("code=%d diagnostics=%q", code, diagnostics)
			}
			if strings.Contains(diagnostics, "pagetty: Saved ") {
				t.Fatalf("normal completion reported with exit code %d: %q", code, diagnostics)
			}
			if tc.pages > 0 && !strings.Contains(diagnostics, "Command failed; saved 1 PNG page in ") {
				t.Fatalf("saved failure record not reported: %q", diagnostics)
			}
			if tc.pages == 0 {
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("render failure created output directory: %v", err)
				}
			}
		})
	}
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, diagnostics := runTestCLI(t, cliArgs([]string{"--output-dir", blocker}, "nonzero"))
	if code != 1 || !strings.Contains(diagnostics, "画像保存失敗:") || !strings.Contains(diagnostics, blocker) || !strings.Contains(diagnostics, "終了コード 7") || strings.Contains(diagnostics, "pagetty: Saved ") {
		t.Fatalf("code=%d diagnostics=%q", code, diagnostics)
	}
	data, err := os.ReadFile(blocker)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}

type cancelOutput struct{ cancel context.CancelFunc }

func (w cancelOutput) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("ready")) {
		w.cancel()
	}
	return len(p), nil
}

type failedOutput struct{ bytes.Buffer }

func (w *failedOutput) Write(p []byte) (int, error) {
	w.Buffer.Write(p)
	if strings.Contains(w.String(), "ready") {
		return 0, errors.New("test output failure")
	}
	return len(p), nil
}

func TestCLIPartialRecords(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupted=%t", interrupted), func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var output io.Writer = &failedOutput{}
			wantCode, message := 1, "test output failure"
			if interrupted {
				output = cancelOutput{cancel}
				wantCode, message = 130, "中断されました"
			}
			var diagnostics bytes.Buffer
			code := runCLI(ctx, cliArgs([]string{"--output-dir", dir}, "block"), nil, output, &diagnostics)
			paths := imageFiles(t, dir, "png")
			if interrupted {
				if code != 130 || !strings.Contains(diagnostics.String(), message) || len(paths) != 0 {
					t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics.String())
				}
				return
			}
			if code != wantCode || !strings.Contains(diagnostics.String(), "PTY実行・記録失敗") || !strings.Contains(diagnostics.String(), message) || len(paths) != 1 || !strings.Contains(paths[0], "-partial_001") {
				t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics.String())
			}
			if !strings.Contains(diagnostics.String(), "Partial capture: 1 PNG page in ") || strings.Contains(diagnostics.String(), "pagetty: Saved ") {
				t.Fatalf("incorrect partial capture summary: %q", diagnostics.String())
			}
			readImage(t, paths[0])
		})
	}
}

func TestCLIInterruptedDoesNotOpenSaveTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var diagnostics bytes.Buffer
	code := runCLI(ctx, cliArgs([]string{"--output-dir", path}, "block"), nil, cancelOutput{cancel}, &diagnostics)
	if code != 130 || !strings.Contains(diagnostics.String(), "中断されました") || strings.Contains(diagnostics.String(), "画像保存失敗") || strings.Contains(diagnostics.String(), "pagetty: Saved ") || strings.Contains(diagnostics.String(), "Partial capture:") {
		t.Fatalf("code=%d diagnostics=%q", code, diagnostics.String())
	}
}

func TestCLIProcessExitCode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		code int
	}{{"text", 0}, {"nonzero", 7}, {"invalid", 2}} {
		args := cliArgs([]string{"--output-dir", t.TempDir()}, tc.mode)
		if tc.mode == "invalid" {
			args = []string{"--invalid"}
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestCLIEntry$", "--", "pagetty-cli-entry"}, args...)...)
		output, err := cmd.CombinedOutput()
		cancel()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatal(err)
			}
			code = exitErr.ExitCode()
		}
		if code != tc.code {
			t.Fatalf("mode=%s code=%d want=%d output=%s", tc.mode, code, tc.code, output)
		}
	}
}

func TestCLIEntry(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "pagetty-cli-entry" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
		}
	}
}

type consoleResponse struct{ prompt, input string }

type consoleResponder struct {
	buf       bytes.Buffer
	input     io.Writer
	responses []consoleResponse
}

func (w *consoleResponder) String() string { return w.buf.String() }

func (w *consoleResponder) Write(p []byte) (int, error) {
	n, _ := w.buf.Write(p)
	for len(w.responses) > 0 && strings.Contains(w.String(), w.responses[0].prompt) {
		if _, err := io.WriteString(w.input, w.responses[0].input); err != nil {
			return n, err
		}
		w.responses = w.responses[1:]
	}
	return n, nil
}

func TestCLIReportDefaults(t *testing.T) {
	code, help, _ := runTestCLI(t, []string{"--help"})
	if code != 0 || !strings.Contains(help, "default 80") || !strings.Contains(help, "default 44") || !strings.Contains(help, "default 16") {
		t.Fatalf("help: %s", help)
	}
	for _, format := range []string{"png", "svg"} {
		dir := t.TempDir()
		code, _, d := runTestCLI(t, cliArgs([]string{"--format", format, "--output-dir", dir}, "missing-glyph"))
		paths := imageFiles(t, dir, format)
		if code != 0 || len(paths) != 1 {
			t.Fatalf("fallback: %d %v %s", code, paths, d)
		}
		readImage(t, paths[0])
	}
}

func TestCLIPageLimitNonInteractive(t *testing.T) {
	for _, tc := range []struct {
		count, pages, code int
		explicit           string
	}{
		{15, 15, 0, ""}, {16, 16, 0, ""}, {17, 16, 1, ""}, {65, 64, 1, "64"}, {2, 1, 1, "1"},
	} {
		dir := t.TempDir()
		options := []string{"--format", "svg", "--max-lines", "1", "--output-dir", dir}
		if tc.explicit != "" {
			options = append(options, "--page-limit", tc.explicit)
		}
		code, output, diagnostics := runTestCLI(t, cliArgs(options, "count", strconv.Itoa(tc.count)))
		paths := imageFiles(t, dir, "svg")
		if code != tc.code || len(paths) != tc.pages || !strings.Contains(output, fmt.Sprintf("line%d", tc.count-1)) {
			t.Fatalf("case=%+v code=%d pages=%d diagnostics=%q", tc, code, len(paths), diagnostics)
		}
		if partial := code == 1; strings.Contains(paths[0], "-partial_") != partial ||
			(strings.Count(diagnostics, "画像生成を打ち切りました") == 1) != partial ||
			strings.Contains(diagnostics, "記録を打ち切りました") || strings.Contains(diagnostics, "選択 [") ||
			strings.Contains(diagnostics, "対象の終了コード 0") != partial {
			t.Fatalf("partial=%t path=%s diagnostics=%q", partial, paths[0], diagnostics)
		}
		for _, path := range paths {
			readImage(t, path)
		}
	}
}

type cancelAtDiagnostic struct {
	bytes.Buffer
	match  string
	cancel context.CancelFunc
}

func (d *cancelAtDiagnostic) Write(p []byte) (int, error) {
	n, err := d.Buffer.Write(p)
	if bytes.Contains(p, []byte(d.match)) {
		d.cancel()
	}
	return n, err
}

func TestCLICancelDuringPostProcessing(t *testing.T) {
	for _, match := range []string{"Rendering 0/", "Saving "} {
		for _, format := range []string{"svg", "png"} {
			t.Run(format+match, func(t *testing.T) {
				dir := t.TempDir()
				keep := filepath.Join(dir, "existing."+format)
				if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				diagnostics := &cancelAtDiagnostic{match: match, cancel: cancel}
				// io.StringWriterを避け、すべての診断をWriteで観測する。
				type writer struct{ io.Writer }
				code := runCLI(ctx, cliArgs([]string{"--format", format, "--output-dir", dir}, "long"), nil, io.Discard, writer{diagnostics})
				entries, err := os.ReadDir(dir)
				if code != 130 || err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(keep) || !strings.Contains(diagnostics.String(), "中断されました") {
					t.Fatalf("code=%d entries=%v err=%v diagnostics=%s", code, entries, err, diagnostics.String())
				}
			})
		}
	}
}
