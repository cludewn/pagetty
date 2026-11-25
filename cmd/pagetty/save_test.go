package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cludewn/pagetty/internal/render"
)

func saveImages(dir, timestamp string, partial bool, images []render.Image) ([]string, error) {
	ctx := context.Background()
	s := imageSave{dir: dir}
	for _, img := range images {
		if err := s.add(ctx, img); err != nil {
			return nil, errors.Join(err, s.close(true))
		}
	}
	err := s.publish(ctx, timestamp, partial)
	if err != nil {
		return nil, errors.Join(err, s.close(true))
	}
	return s.published, s.close(false)
}

func TestSaveImagesCollisionAndConcurrency(t *testing.T) {
	dir := t.TempDir()
	prefix := "20261122-123045"
	existing := filepath.Join(dir, prefix+"_002.svg")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	const workers = 6
	type result struct {
		paths []string
		err   error
		data  string
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			data := fmt.Sprintf("worker%d", i)
			paths, err := saveImages(dir, prefix, i%2 == 0, []render.Image{
				{Format: render.PNG, Data: []byte(data)}, {Format: render.PNG, Data: []byte(data)},
			})
			results <- result{paths, err, data}
		})
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{existing: true}
	for result := range results {
		if result.err != nil || len(result.paths) != 2 {
			t.Fatalf("images=%v err=%v data=%q", result.paths, result.err, result.data)
		}
		group, ok := strings.CutSuffix(filepath.Base(result.paths[0]), "_001.png")
		if !ok || !strings.HasPrefix(group, prefix+"-") || filepath.Base(result.paths[1]) != group+"_002.png" {
			t.Fatalf("expected one collision suffix per run: %v", result.paths)
		}
		for _, path := range result.paths {
			if seen[path] {
				t.Fatalf("duplicate or ungrouped path: %s", path)
			}
			seen[path] = true
			data, err := os.ReadFile(path)
			if err != nil || string(data) != result.data {
				t.Fatalf("path=%s data=%q err=%v", path, data, err)
			}
		}
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file overwritten: %q %v", data, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1+workers*2 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestSaveNoImagesCreatesNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "unused")
	paths, err := saveImages(dir, "unused", false, nil)
	if err != nil || len(paths) != 0 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unused directory created: %v", err)
	}
}

func TestSaveShortNames(t *testing.T) {
	dir := t.TempDir()
	timestamp := "20261122-123045"
	imgs := []render.Image{{Format: render.PNG}, {Format: render.PNG}}
	for i, want := range []string{timestamp, timestamp + "-2-partial"} {
		paths, err := saveImages(dir, timestamp, i != 0, imgs)
		if err != nil || len(paths) != 2 || filepath.Base(paths[0]) != want+"_001.png" || filepath.Base(paths[1]) != want+"_002.png" {
			t.Fatalf("paths=%v err=%v", paths, err)
		}
	}
}

type cancelWhenPublished struct {
	context.Context
	path string
}

func (c cancelWhenPublished) Err() error {
	if _, err := os.Stat(c.path); err == nil {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestSaveCancellationRemovesPublishedPages(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "20261122-123045_001.png")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	save := imageSave{dir: dir}
	for range 2 {
		if err := save.add(context.Background(), render.Image{Format: render.PNG, Data: []byte("new")}); err != nil {
			t.Fatal(err)
		}
	}
	first := filepath.Join(dir, "20261122-123045-2_001.png")
	ctx := cancelWhenPublished{Context: context.Background(), path: first}
	if err := save.publish(ctx, "20261122-123045", false); !errors.Is(err, context.Canceled) || len(save.published) != 1 {
		t.Fatalf("published=%v err=%v", save.published, err)
	}
	if err := save.close(true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	data, readErr := os.ReadFile(existing)
	if err != nil || readErr != nil || len(entries) != 1 || string(data) != "keep" {
		t.Fatalf("entries=%v data=%q errors=%v", entries, data, errors.Join(err, readErr))
	}
}

type cancelOnCheck struct {
	context.Context
	checks int
}

func (c *cancelOnCheck) Err() error {
	c.checks--
	if c.checks <= 0 {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestSaveCancellationRemovesIncompleteStage(t *testing.T) {
	save := imageSave{dir: t.TempDir()}
	ctx := &cancelOnCheck{Context: context.Background(), checks: 3}
	err := save.add(ctx, render.Image{Format: render.SVG, Data: make([]byte, 128*1024)})
	if info, statErr := os.Stat(save.incomplete); statErr != nil || info.Size() != 64*1024 || !errors.Is(err, context.Canceled) {
		t.Fatalf("during write: file=%v write error=%v stat error=%v", info, err, statErr)
	}
	if err := save.close(true); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(save.dir); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestSaveFailureDiscardsCompletedPublication(t *testing.T) {
	save := imageSave{dir: t.TempDir()}
	ctx := context.Background()
	for range 2 {
		if err := save.add(ctx, render.Image{Format: render.SVG, Data: []byte("image")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(save.staged[1].path); err != nil {
		t.Fatal(err)
	}
	if err := save.publish(ctx, "20261122-123045", false); err == nil || len(save.published) != 1 {
		t.Fatalf("published=%v err=%v", save.published, err)
	}
	if err := save.close(true); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(save.dir); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestPublishImageDoesNotReplaceExisting(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "incoming"), filepath.Join(dir, "existing")
	for path, text := range map[string]string{src: "new", dst: "keep"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(src)
	if err != nil {
		t.Fatal(err)
	}
	img := stagedImage{path: src, info: info}
	if err := img.publish(dst); err == nil {
		t.Fatal("existing output overwritten")
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "keep" {
		t.Fatalf("existing data=%q err=%v", data, err)
	}
}

func TestCLISaveFailureDiscardsRun(t *testing.T) {
	for _, phase := range []struct {
		name, trigger string
		blockSecond   bool
	}{
		{"second page staging", "Rendering 1/2", true},
		{"second page publication", "Saving 2 pages", false},
	} {
		t.Run(phase.name, func(t *testing.T) {
			dir := t.TempDir()
			existing := filepath.Join(dir, "existing.svg")
			if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			var staging string
			diagnostics := &saveFailureDiagnostic{
				trigger: phase.trigger,
				fail: func() error {
					paths, err := filepath.Glob(filepath.Join(dir, ".pagetty-*"))
					if err != nil || len(paths) != 1 {
						return fmt.Errorf("staging directories: %v (%v)", paths, err)
					}
					staging = paths[0]
					if _, err := os.Stat(filepath.Join(staging, "001")); err != nil {
						return err
					}
					second := filepath.Join(staging, "002")
					if phase.blockSecond {
						return os.WriteFile(second, []byte("keep blocker"), 0600)
					}
					return os.Remove(second)
				},
			}
			args := cliArgs([]string{"--format", "svg", "--max-lines", "1", "--output-dir", dir}, "count", "2")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			code := runCLI(ctx, args, nil, &bytes.Buffer{}, diagnostics)
			output := diagnostics.String()
			if diagnostics.fail != nil || diagnostics.err != nil || code != 1 ||
				!strings.Contains(output, "画像保存失敗") || !strings.Contains(output, filepath.Join(staging, "002")) ||
				strings.Contains(output, "pagetty: Output:") || strings.Contains(output, "pagetty: Saved ") ||
				strings.Contains(output, "Partial capture:") {
				t.Fatalf("code=%d injection=%v diagnostics=%q", code, diagnostics.err, output)
			}
			if data, err := os.ReadFile(existing); err != nil || string(data) != "keep" {
				t.Fatalf("existing data=%q err=%v", data, err)
			}
			want := 1
			if phase.blockSecond {
				// 自分で作っていない障害物は残し、作業用ディレクトリを消せなかったと通知する。
				want++
				children, err := os.ReadDir(staging)
				data, readErr := os.ReadFile(filepath.Join(staging, "002"))
				if err != nil || readErr != nil || len(children) != 1 || string(data) != "keep blocker" ||
					!strings.Contains(output, "保存の後処理に失敗しました") || strings.Contains(output, "Saving 1") {
					t.Fatalf("staging=%v blocker=%q errors=%v diagnostics=%q", children, data, errors.Join(err, readErr), output)
				}
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != want {
				t.Fatalf("entries=%v err=%v diagnostics=%q", entries, err, output)
			}
		})
	}
}

type saveFailureDiagnostic struct {
	bytes.Buffer
	trigger string
	fail    func() error
	err     error
}

func (d *saveFailureDiagnostic) Write(p []byte) (int, error) {
	if d.fail != nil && bytes.Contains(p, []byte(d.trigger)) {
		d.err = d.fail()
		d.fail = nil
	}
	return d.Buffer.Write(p)
}

func TestSaveRollbackPreservesReplacement(t *testing.T) {
	dir := t.TempDir()
	save := imageSave{dir: dir}
	ctx := t.Context()
	if err := save.add(ctx, render.Image{Format: render.SVG, Data: []byte("generated")}); err != nil {
		t.Fatal(err)
	}
	if err := save.publish(ctx, "20261122-123045", false); err != nil {
		t.Fatal(err)
	}
	path := save.published[0]
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := save.close(true); err == nil || !strings.Contains(err.Error(), "置き換えられたため削除できません") {
		t.Fatalf("rollback error=%v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "replacement" {
		t.Fatalf("replacement=%q err=%v", got, err)
	}
}
