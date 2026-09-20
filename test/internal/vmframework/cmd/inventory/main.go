// Command inventory emits a reproducible VM harness source/duplicate inventory.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/inventory"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	report, err := inventory.Scan(*root)
	if err == nil {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
