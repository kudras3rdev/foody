// Command foody is the Foody CLI: eval, reprocess, seed and label
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

const usage = `usage: foody <command>

commands:
  eval       run the golden set through a model configuration (FR-20)
  reprocess  reprocess entries with another model configuration (FR-18)
  seed       load the starter catalog (FR-08)
  label      read nutrition labels from photos and show the checks (prompt debugging)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch cmd := os.Args[1]; cmd {
	case "label":
		if err := runLabel(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "foody label: %v\n", err)
			os.Exit(1)
		}
	case "eval", "reprocess", "seed":
		fmt.Fprintf(os.Stderr, "foody %s: not implemented\n", cmd)
		os.Exit(1)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "foody: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}
