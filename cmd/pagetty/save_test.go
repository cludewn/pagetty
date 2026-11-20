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
	prefix := "capture-20261120-120000-test"
	existing := filepath.Join(dir, prefix+"_001.png")
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
			paths, err := saveImages(dir, prefix, []render.Image{
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
		for _, path := range result.paths {
			if seen[path] || !strings.Contains(filepath.Base(path), prefix) {
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
	if paths := imageFiles(t, dir, "png"); len(paths) != 1+workers*2 {
		t.Fatalf("paths=%v", paths)
	}
}

func TestSaveNoImagesCreatesNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "unused")
	paths, err := saveImages(dir, "unused", nil)
	if err != nil || len(paths) != 0 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unused directory created: %v", err)
	}
}
