package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/nirnx/zester/cmd/zester/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// Classified peel-result codes (see cmd.ExitCodeError): 2 = peels
		// failed, 3 = peels unreachable/missing, 4 = both. Everything else
		// stays the generic 1.
		var ec *cmd.ExitCodeError
		if errors.As(err, &ec) {
			os.Exit(ec.Code)
		}
		os.Exit(1)
	}
}
