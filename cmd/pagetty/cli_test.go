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
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

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
		case "text":
			fmt.Print("日本語ABCDEF")
		case "args":
			data, _ := json.Marshal(os.Args[i+2:])
			fmt.Print(string(data))
		case "lines":
			for i := range 5 {
				fmt.Printf("line%d\n", i)
			}
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
	result := append([]string{"capture"}, options...)
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
	for _, args := range [][]string{{"--help"}, {"-h"}, {"capture", "--help"}, {"capture", "-h"}} {
		code, output, diagnostics := runTestCLI(t, args)
		if code != 0 || !strings.Contains(output, "pagetty capture") || diagnostics != "" {
			t.Fatalf("args=%q code=%d output=%q diagnostics=%q", args, code, output, diagnostics)
		}
	}
	for _, args := range [][]string{
		nil, {"unknown"}, {"--help", "extra"}, {"capture"}, {"capture", "--"},
		{"capture", "echo", "bad"}, {"capture", "unexpected", "--", "echo"},
		{"capture", "--unknown", "--", "echo"}, {"capture", "--columns", "x", "--", "echo"},
	} {
		code, _, diagnostics := runTestCLI(t, args)
		if code != 2 || diagnostics == "" {
			t.Fatalf("args=%q code=%d diagnostics=%q", args, code, diagnostics)
		}
	}
	for _, options := range [][]string{
		{"--format", "jpeg"}, {"--columns", "1"}, {"--columns", "32768"},
		{"--max-lines", "0"}, {"--max-lines", "-1"}, {"--output-dir", ""},
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
			if len(paths) != 2 || !strings.Contains(diagnostics, "2ページの"+format) {
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
		if w != 1032 || h != wantHeight {
			t.Fatalf("page=%d size=%dx%d", i, w, h)
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
		{[]string{"capture", "--", filepath.Join(dir, "missing-command")}, 1},
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
	if w != 1032 || h != 60 {
		t.Fatalf("default size=%dx%d", w, h)
	}
	longDir := filepath.Join(dir, "long")
	code, _, diagnostics = runTestCLI(t, cliArgs([]string{"--output-dir", longDir}, "long"))
	paths = imageFiles(t, longDir, "png")
	if code != 0 || len(paths) != 3 {
		t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics)
	}
	for i, path := range paths {
		_, h := readImage(t, path)
		want := 1152
		if i == 2 {
			want = 60
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
		{"render failure", "nonzero", "レンダリング失敗", []string{"--columns", "32767"}, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "images")
			options := append([]string{"--output-dir", dir}, tc.options...)
			code, _, diagnostics := runTestCLI(t, cliArgs(options, tc.mode))
			if code != tc.code || !strings.Contains(diagnostics, tc.message) || len(imageFiles(t, dir, "png")) != tc.pages {
				t.Fatalf("code=%d diagnostics=%q", code, diagnostics)
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
	if code != 1 || !strings.Contains(diagnostics, "画像保存失敗（0/1ページ保存）") || !strings.Contains(diagnostics, "終了コード 7") || strings.Contains(diagnostics, "画像を保存しました") {
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
			if code != wantCode || !strings.Contains(diagnostics.String(), "PTY実行・記録失敗") || !strings.Contains(diagnostics.String(), message) || len(paths) != 1 || !strings.Contains(paths[0], "-partial_001") {
				t.Fatalf("code=%d paths=%v diagnostics=%q", code, paths, diagnostics.String())
			}
			readImage(t, paths[0])
		})
	}
}

func TestCLIInterruptedSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var diagnostics bytes.Buffer
	code := runCLI(ctx, cliArgs([]string{"--output-dir", path}, "block"), nil, cancelOutput{cancel}, &diagnostics)
	if code != 1 || !strings.Contains(diagnostics.String(), "中断されました") || !strings.Contains(diagnostics.String(), "画像保存失敗") || strings.Contains(diagnostics.String(), "画像を保存しました") {
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
			args = []string{"capture", "--invalid"}
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
