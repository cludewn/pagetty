package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cludewn/pagetty/internal/render"
)

func saveImages(dir, prefix string, images []render.Image) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	var saved []string
	for i, img := range images {
		base := fmt.Sprintf("%s_%03d", prefix, i+1)
		var file *os.File
		var path string
		for collision := 0; ; collision++ {
			name := base
			if collision > 0 {
				name += fmt.Sprintf("-%d", collision)
			}
			path = filepath.Join(dir, name+"."+string(img.Format))
			var err error
			file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return saved, err
			}
			break
		}
		n, writeErr := file.Write(img.Data)
		if writeErr == nil && n != len(img.Data) {
			writeErr = io.ErrShortWrite
		}
		if err := errors.Join(writeErr, file.Close()); err != nil {
			// 保存済みページは残し、この処理が作った不完全なファイルだけを消す。
			return saved, fmt.Errorf("%s: %w", path, errors.Join(err, os.Remove(path)))
		}
		saved = append(saved, path)
	}
	return saved, nil
}
