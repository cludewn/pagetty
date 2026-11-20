package main

import (
	"context"
	"os"
)

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
