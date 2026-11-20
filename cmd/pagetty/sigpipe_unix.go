//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func preventBrokenPipeExit() {
	// Ignoreだと子にも無視設定を継承するため、Notifyで書き込み失敗を既存の処理へ渡す。
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
}
