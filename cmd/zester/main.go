package main

import (
	"fmt"
	"os"

	"github.com/ptorbus/zester/cmd/zester/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
