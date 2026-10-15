package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/kb"
)

func kbCmd() *cobra.Command {
	var sdk string
	cmd := &cobra.Command{
		Use:   "kb [detector-id]",
		Short: "Show the fix guidance for a detector, or list them all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				all, err := kb.AllGuidance()
				if err != nil {
					return err
				}
				for _, id := range kb.GuidanceIDs() {
					fmt.Printf("%-8s %s\n", id, all[id].Title)
				}
				return nil
			}
			g, err := kb.GuidanceFor(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s\n\nWhy it costs: %s\nFix: %s\nTradeoffs: %s\n\n%s\n", g.ID, g.Title, g.WhyItCosts, g.Fix, g.Tradeoffs, g.Body)
			if s := g.Snippet(sdk); s != "" {
				fmt.Printf("\n%s", s)
			}
			for _, d := range g.Docs {
				fmt.Printf("\nDocs: %s", d.KB)
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().StringVar(&sdk, "sdk", "go", "SDK for the before-and-after snippet")
	return cmd
}
