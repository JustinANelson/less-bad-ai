package main

import (
	"fmt"
	"os"

	"github.com/jnels/less-bad-ai/pkg/cli"
)

func main() {
	if err := cli.New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
