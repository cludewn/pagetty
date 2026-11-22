package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cludewn/pagetty/internal/render"
)

func saveImages(dir, timestamp string, partial bool, images []render.Image) (saved []string, err error) {
	if len(images) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	prefix, lock, err := reserveOutputGroup(dir, timestamp)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.Remove(lock)) }()
	if partial {
		prefix += "-partial"
	}
	for i, img := range images {
		path := filepath.Join(dir, fmt.Sprintf("%s_%03d.%s", prefix, i+1, img.Format))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return saved, err
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

// 日時が同じ実行は、形式やpartialの有無にかかわらず別の組として確保する。
func reserveOutputGroup(dir, timestamp string) (prefix, lock string, err error) {
	for index := 1; ; index++ {
		prefix = timestamp
		if index > 1 {
			prefix += fmt.Sprintf("-%d", index)
		}
		lock = filepath.Join(dir, "."+prefix+".lock")
		if err := os.Mkdir(lock, 0700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", "", err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", "", errors.Join(err, os.Remove(lock))
		}
		taken := false
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, prefix+"_") || strings.HasPrefix(name, prefix+"-partial_") {
				taken = true
				break
			}
		}
		if !taken {
			return prefix, lock, nil
		}
		if err := os.Remove(lock); err != nil {
			return "", "", err
		}
	}
}
