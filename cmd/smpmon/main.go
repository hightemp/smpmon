// Command smpmon is a configurable terminal resource monitor.
package main

import (
	"fmt"
	"os"

	"github.com/hightemp/smpmon/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "smpmon:", err)
		os.Exit(1)
	}
}
