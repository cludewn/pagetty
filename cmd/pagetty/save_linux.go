package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func (img *stagedImage) publish(target string) error {
	err := unix.Renameat2(unix.AT_FDCWD, img.path, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		err = os.Link(img.path, target)
	} else if err != nil {
		err = &os.LinkError{Op: "publish", Old: img.path, New: target, Err: err}
	}
	return err
}
