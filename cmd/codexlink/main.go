package main

import (
	"context"
	"os"

	"github.com/joeykchen/codexlink/internal/cli"
)

func main() {
	os.Exit(cli.New().Run(context.Background(), os.Args[1:]))
}
