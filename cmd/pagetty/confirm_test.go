package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/term"
)

type pagePromptCase struct {
	mode, total, limit, answer string
	code, pages                int
	partial                    bool
}

var pagePromptCases = []pagePromptCase{
	{"page-equal", "2", "2", "", 7, 2, false},
	{"page-all", "3", "2", "a\r", 7, 3, false},
	{"page-limit", "3", "2", "?\r", 7, 2, true},
	{"page-none", "3", "2", "n\r", 7, 0, false},
	{"page-empty", "3", "2", "\r", 7, 0, false},
	{"page-eof", "3", "2", "\x04", 7, 0, false},
	{"page-interrupt", "3", "2", "\x03", 130, 0, false},
	{"page-hard", "65", "64", "a\r", 7, 0, false},
	{"page-missing", "1650", "2", "n\r", 1, 0, true},
}

func (tc pagePromptCase) responses(eof string) []consoleResponse {
	switch tc.mode {
	case "page-equal":
		return nil
	case "page-hard":
		return []consoleResponse{{"選択 [l/N]:", tc.answer}, {"選択が無効です", "n\r"}}
	case "page-limit":
		return []consoleResponse{{"選択 [a/l/N]:", tc.answer}, {"選択が無効です", "l\r"}}
	case "page-eof":
		return []consoleResponse{{"選択 [a/l/N]:", eof}}
	default:
		return []consoleResponse{{"選択 [a/l/N]:", tc.answer}}
	}
}

func (tc pagePromptCase) check(t *testing.T, dir string, out *consoleResponder) {
	t.Helper()
	output := out.String()
	if len(out.responses) != 0 || !strings.Contains(output, fmt.Sprintf("CLI_RESULT:%d:restored=true", tc.code)) {
		t.Fatalf("unconsumed responses=%d output=%q", len(out.responses), output)
	}
	prompt := strings.Index(output, "選択 [")
	rendering := strings.Index(output, "Rendering")
	if (prompt == -1) != (tc.mode == "page-equal") || (rendering == -1) != (tc.pages == 0) ||
		prompt >= 0 && rendering >= 0 && rendering < prompt {
		t.Fatalf("unexpected confirmation or rendering order: %q", output)
	}
	if prompt >= 0 && !strings.Contains(output, "※ 生成時の容量制限により、保存枚数が少なくなる場合があります。") {
		t.Fatalf("missing capacity notice: %q", output)
	}
	if tc.mode == "page-hard" && (!strings.Contains(output, "先頭の64枚") || strings.Contains(output, "[a]")) ||
		tc.mode == "page-missing" && !strings.Contains(output, "記録に欠落がある") {
		t.Fatalf("inaccurate confirmation: %q", output)
	}
	paths := imageFiles(t, dir, "svg")
	if len(paths) != tc.pages {
		t.Fatalf("pages=%d want=%d output=%q", len(paths), tc.pages, output)
	}
	for _, path := range paths {
		if strings.Contains(path, "-partial_") != tc.partial {
			t.Fatalf("partial=%v path=%q", tc.partial, path)
		}
		readImage(t, path)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "existing.png"))
	if err != nil || string(contents) != "keep" {
		t.Fatalf("old file=%q err=%v", contents, err)
	}
}

func TestCLIPagePromptProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "pagetty-page-prompt" {
		return
	}
	mode, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	for _, tc := range pagePromptCases {
		if tc.mode != mode {
			continue
		}
		before, err := term.GetState(int(os.Stdin.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		// 製品と同様に、テストプロセスが終了するまで購読を維持する。
		ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		opts := []string{"--format", "svg", "--max-lines", "1", "--page-limit", tc.limit, "--output-dir", dir}
		code := runCLI(ctx, cliArgs(opts, "count", tc.total, "7"), os.Stdin, os.Stdout, os.Stderr)
		after, err := term.GetState(int(os.Stdin.Fd()))
		fmt.Printf("\nCLI_RESULT:%d:restored=%t\n", code, err == nil && reflect.DeepEqual(before, after))
		return
	}
	t.Fatal("unknown confirmation test", mode)
}
