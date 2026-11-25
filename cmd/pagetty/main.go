package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	preventBrokenPipeExit()
	// 保存確定後も、終了直前までシグナルでプロセスが強制終了しないようにする。
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	os.Exit(runCLI(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
