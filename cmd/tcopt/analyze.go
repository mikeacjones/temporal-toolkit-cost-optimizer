package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/report"
)

// targetFlags are the connection flags shared by analyze and the TUI.
type targetFlags struct {
	profile    string
	configFile string
	namespace  string
	rps        float64
	noCache    bool
}

func (f *targetFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.profile, "profile", "", "Temporal CLI profile for the Namespace to analyze (or set TEMPORAL_PROFILE, or TEMPORAL_ADDRESS without a profile)")
	cmd.Flags().StringVar(&f.configFile, "config-file", "", "path to temporal.toml (default: the Temporal CLI's)")
	cmd.Flags().StringVarP(&f.namespace, "namespace", "n", "", "override the profile's Namespace")
	cmd.Flags().Float64Var(&f.rps, "rps", history.DefaultRPS, "maximum requests per second against the Namespace")
	cmd.Flags().BoolVar(&f.noCache, "no-cache", false, "don't read or write the history cache")
}

// dial connects to the target named by flags or TEMPORAL_* variables, or
// else to the target saved by the TUI's setup.
func (f *targetFlags) dial() (*config.Target, *history.Reader, error) {
	var t *config.Target
	var err error
	if config.Explicit(f.profile) {
		t, err = config.DialProfile(f.profile, f.configFile, f.namespace)
	} else {
		var st *config.Settings
		st, err = config.LoadSettings()
		if err == nil && st.Target.Source == "" {
			err = errors.New("no Namespace set up yet; run tcopt to set one up, or pass --profile")
		}
		if err == nil {
			t, err = config.DialSaved(st.Target)
			if errors.Is(err, config.ErrKeyMissing) {
				err = fmt.Errorf("the API key for %s expired; run tcopt to create a new one", st.Target.Namespace)
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	cache := history.DefaultCacheDir()
	if f.noCache {
		cache = ""
	}
	return t, history.NewReader(t.Client.WorkflowService(), t.Namespace, f.rps, cache), nil
}

func analyzeCmd() *cobra.Command {
	var (
		tf        targetFlags
		opts      analyze.Options
		types     string
		detectors string
		window    time.Duration
		format    string
		out       string
		repos     []string
	)
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Analyze a Namespace without the TUI and write a report",
		Long: `Analyze a Namespace without the TUI and write a report.

tcopt only reads the Namespace. Reading histories, listing, and counting
executions aren't billable Actions, but they do use the Namespace's requests
per second, so tcopt rate-limits itself (--rps).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			t, r, err := tf.dial()
			if err != nil {
				return err
			}
			defer t.Close()

			opts.Profile = t.Profile
			opts.Window = window
			opts.Types = splitList(types)
			opts.Detectors = splitList(detectors)
			opts.RepoPaths = config.ResolveRepos(repos)

			fmt.Fprintf(os.Stderr, "Analyzing %s at %s with profile %s\n", t.Namespace, t.Address, t.Profile)
			rep, err := analyze.Run(ctx, r, opts, stderrProgress())
			if err != nil {
				return err
			}

			var w io.Writer = os.Stdout
			if out != "" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer f.Close()
				w = f
			}
			if err := report.Write(w, rep, format); err != nil {
				return err
			}
			if out != "" {
				fmt.Fprintf(os.Stderr, "Wrote %s\n", out)
			}
			return nil
		},
	}
	tf.register(cmd)
	cmd.Flags().StringVar(&types, "types", "", "comma-separated Workflow Types (default: the busiest --top types)")
	cmd.Flags().IntVar(&opts.TopTypes, "top", 5, "how many of the busiest Workflow Types to analyze when --types isn't set")
	cmd.Flags().IntVar(&opts.SampleSize, "sample", 200, "executions to sample per Workflow Type")
	cmd.Flags().DurationVar(&window, "window", 7*24*time.Hour, "look at executions started within this window")
	cmd.Flags().StringVar(&detectors, "detectors", "", "comma-separated detector IDs (default: all)")
	cmd.Flags().IntVar(&opts.Parallel, "parallel", 4, "history fetches in flight")
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "a repo to read SDK versions from (repeatable; default: the saved repos)")
	cmd.Flags().BoolVar(&opts.IncludePreview, "include-preview", false, "show findings whose only fix is a pre-release feature")
	cmd.Flags().StringVarP(&format, "format", "f", "md", "report format: "+strings.Join(report.Formats, ", "))
	cmd.Flags().StringVarP(&out, "out", "o", "", "write the report to a file instead of stdout")
	return cmd
}

// stderrProgress prints one updating line on a terminal, and stage changes
// otherwise.
func stderrProgress() func(analyze.Progress) {
	tty := term.IsTerminal(int(os.Stderr.Fd()))
	last := ""
	return func(p analyze.Progress) {
		var line string
		switch p.Stage {
		case "rank":
			line = "Ranking Workflow Types"
		case "sample":
			line = "Sampling " + p.WorkflowType
		case "fetch":
			line = fmt.Sprintf("Fetching %s histories: %d of %d", p.WorkflowType, p.Done, p.Total)
		case "children":
			line = "Fetching Child Workflow histories for " + p.WorkflowType
		case "detect":
			line = "Running detectors on " + p.WorkflowType
		}
		if tty {
			fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
			if p.Stage == "detect" {
				fmt.Fprintln(os.Stderr)
			}
			return
		}
		if p.Stage != last {
			fmt.Fprintln(os.Stderr, line)
			last = p.Stage
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
