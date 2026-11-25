package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cludewn/pagetty/internal/render"
)

type stagedImage struct {
	path     string
	format   render.Format
	info     os.FileInfo
	file     *os.File // Windowsでは公開前に確認した同じファイルを閉じずに保持する。
	replaced bool
}

type imageSave struct {
	dir, staging, lock string
	incomplete         string
	staged             []stagedImage
	published          []string
}

func (s *imageSave) add(ctx context.Context, img render.Image) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.staging == "" {
		if err := os.MkdirAll(s.dir, 0755); err != nil {
			return err
		}
		dir, err := os.MkdirTemp(s.dir, ".pagetty-")
		if err != nil {
			return err
		}
		s.staging = dir
	}
	path := filepath.Join(s.staging, fmt.Sprintf("%03d", len(s.staged)+1))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	s.incomplete = path
	var writeErr error
	for data := img.Data; len(data) != 0; {
		if writeErr = ctx.Err(); writeErr != nil {
			break
		}
		chunk := data[:min(len(data), 64*1024)]
		n, err := file.Write(chunk)
		if err != nil {
			writeErr = err
			break
		}
		if n != len(chunk) {
			writeErr = io.ErrShortWrite
			break
		}
		data = data[n:]
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if err := errors.Join(writeErr, statErr, closeErr); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	s.incomplete = ""
	s.staged = append(s.staged, stagedImage{path: path, format: img.Format, info: info})
	return ctx.Err()
}

func (s *imageSave) publish(ctx context.Context, timestamp string, partial bool) error {
	if len(s.staged) == 0 {
		return nil
	}
	prefix, lock, err := reserveOutputGroup(ctx, s.dir, timestamp)
	if err != nil {
		return err
	}
	s.lock = lock
	if partial {
		prefix += "-partial"
	}
	for i := range s.staged {
		if err := ctx.Err(); err != nil {
			return err
		}
		img := &s.staged[i]
		path := filepath.Join(s.dir, fmt.Sprintf("%s_%03d.%s", prefix, i+1, img.format))
		if err := img.publish(path); err != nil {
			return err
		}
		s.published = append(s.published, path)
	}
	return ctx.Err()
}

func (s *imageSave) close(discard bool) error {
	var result error
	if discard {
		for i, path := range s.published {
			info, err := os.Lstat(path)
			if err == nil {
				img := s.staged[i]
				expected := img.info
				if img.file != nil {
					expected, err = img.file.Stat()
				}
				switch {
				case err != nil:
					err = fmt.Errorf("公開画像の同一性を取得できません: %s: %w", path, err)
				case os.SameFile(info, expected):
					err = os.Remove(path)
				default:
					err = fmt.Errorf("公開画像が置き換えられたため削除できません: %s", path)
				}
			}
			result = errors.Join(result, ignoreMissing(err))
		}
	}
	for _, img := range s.staged {
		if img.file != nil {
			result = errors.Join(result, img.file.Close())
		}
		if !img.replaced {
			result = errors.Join(result, ignoreMissing(os.Remove(img.path)))
		}
	}
	if s.incomplete != "" {
		result = errors.Join(result, ignoreMissing(os.Remove(s.incomplete)))
	}
	if s.staging != "" {
		result = errors.Join(result, os.Remove(s.staging))
	}
	if s.lock != "" {
		result = errors.Join(result, os.Remove(s.lock))
	}
	return result
}

func ignoreMissing(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// 日時が同じ実行は、形式やpartialの有無にかかわらず別の組として確保する。
func reserveOutputGroup(ctx context.Context, dir, timestamp string) (prefix, lock string, err error) {
	for index := 1; ; index++ {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		prefix = timestamp
		if index > 1 {
			prefix += fmt.Sprintf("-%d", index)
		}
		lock = filepath.Join(dir, "."+prefix+".lock")
		if err := acquireOutputLock(ctx, dir, prefix, lock); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", "", err
		}
		taken, err := outputGroupExists(ctx, dir, prefix)
		if err != nil {
			return "", "", errors.Join(err, os.Remove(lock))
		}
		if !taken {
			return prefix, lock, nil
		}
		if err := os.Remove(lock); err != nil {
			return "", "", err
		}
	}
}

func acquireOutputLock(ctx context.Context, dir, prefix, lock string) error {
	err := os.Mkdir(lock, 0700)
	if !errors.Is(err, os.ErrPermission) {
		return err
	}
	// Windowsでは削除中のロックを作ろうとするとアクセス拒否になり得る。
	if _, statErr := os.Lstat(lock); statErr == nil {
		return os.ErrExist
	}
	taken, readErr := outputGroupExists(ctx, dir, prefix)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if taken {
		return os.ErrExist
	}
	timer := time.NewTimer(2 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return os.Mkdir(lock, 0700)
	}
}

func outputGroupExists(ctx context.Context, dir, prefix string) (found bool, err error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		names, err := f.Readdirnames(128)
		for _, name := range names {
			if strings.HasPrefix(name, prefix+"_") || strings.HasPrefix(name, prefix+"-partial_") {
				return true, nil
			}
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}
