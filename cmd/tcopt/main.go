// Command tcopt finds patterns in a Namespace's Workflow histories that waste
// Temporal Cloud Actions, and estimates what fixing them would save.
package main

import (
	"fmt"
	"os"
)

func main() {
	// Running tcopt with no subcommand opens the TUI.
	root := tuiCmd()
	root.AddCommand(analyzeCmd(), kbCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "tcopt:", err)
		os.Exit(1)
	}
}
