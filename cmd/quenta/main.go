package main

import (
	"os"

	"github.com/TadahiroYamamura/quenta/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.DefaultEnv(os.Getenv, os.Stdout, os.Stderr)))
}
