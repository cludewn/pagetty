package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cludewn/pagetty/internal/render"
)

func TestSaveRollbackPreservesStagedReplacement(t *testing.T) {
	save := imageSave{dir: t.TempDir()}
	ctx := t.Context()
	for range 3 {
		if err := save.add(ctx, render.Image{Format: render.SVG, Data: []byte("generated")}); err != nil {
			t.Fatal(err)
		}
	}

	replaced := save.staged[1].path
	// 元のファイルを別の場所に残し、置換前のファイルIDが再利用されないようにする。
	if err := os.Rename(replaced, filepath.Join(t.TempDir(), "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replaced, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	err := save.publish(ctx, "20261122-123045", false)
	if err == nil || !strings.Contains(err.Error(), "一時画像が置き換えられた") || len(save.published) != 1 {
		t.Fatalf("publish error=%v published=%v", err, save.published)
	}

	err = save.close(true)
	if got, readErr := os.ReadFile(replaced); readErr != nil || string(got) != "replacement" {
		t.Fatalf("replacement=%q read error=%v cleanup error=%v", got, readErr, err)
	}
	var pathErr *os.PathError
	if err != nil && (!errors.As(err, &pathErr) || pathErr.Path != save.staging) {
		t.Fatalf("unexpected cleanup error=%v", err)
	}
	for _, path := range []string{save.published[0], save.staged[2].path, save.lock} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s remains or could not be checked: %v", path, err)
		}
	}
}
