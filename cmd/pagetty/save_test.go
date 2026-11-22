package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cludewn/pagetty/internal/render"
)

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
			t.Fatalf("result=%+v", result)
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
