package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/tui"
)

// tuiCmd is the root command: running tcopt opens the TUI.
func tuiCmd() *cobra.Command {
	var (
		tf      targetFlags
		opts    analyze.Options
		types   string
		window  time.Duration
		reduced bool
		repos   []string
	)
	cmd := &cobra.Command{
		Use:           "tcopt",
		Short:         "Temporal cost optimizer: find patterns in Workflow histories that waste Actions",
		Long:          "Temporal cost optimizer. With no subcommand, tcopt opens the TUI.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			// A profile or TEMPORAL_* variables pick the Namespace directly.
			// Otherwise the TUI reconnects to the saved one, or runs setup.
			var target *config.Target
			if config.Explicit(tf.profile) {
				t, err := config.DialProfile(tf.profile, tf.configFile, tf.namespace)
				if err != nil {
					return err
				}
				target = t
			}
			opts.Window = window
			opts.Types = splitList(types)
			cache := history.DefaultCacheDir()
			if tf.noCache {
				cache = ""
			}
			return tui.Run(ctx, tui.Options{
				Target:   target,
				Analyze:  opts,
				Repos:    repos,
				RPS:      tf.rps,
				CacheDir: cache,
				Motion:   tui.MotionAllowed(reduced, term.IsTerminal(int(os.Stdout.Fd()))),
			})
		},
	}
	tf.register(cmd)
	cmd.Flags().StringVar(&types, "types", "", "comma-separated Workflow Types (default: the busiest --top types)")
	cmd.Flags().IntVar(&opts.TopTypes, "top", 5, "how many of the busiest Workflow Types to analyze when --types isn't set")
	cmd.Flags().IntVar(&opts.SampleSize, "sample", 200, "executions to sample per Workflow Type")
	cmd.Flags().DurationVar(&window, "window", 7*24*time.Hour, "look at executions started within this window")
	cmd.Flags().IntVar(&opts.Parallel, "parallel", 4, "history fetches in flight")
	cmd.Flags().BoolVar(&reduced, "reduced-motion", false, "no starfield or Ziggy motion")
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "a repo to read SDK versions from (repeatable; default: the saved repos)")
	cmd.Flags().BoolVar(&opts.IncludePreview, "include-preview", false, "show findings whose only fix is a pre-release feature")
	return cmd
}
