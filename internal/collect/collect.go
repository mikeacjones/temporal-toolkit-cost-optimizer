// Package collect builds the checks that read something other than
// sampled histories: Namespace config, Cloud metrics, and code. The
// headless command and the TUI share it, so both run the same checks from
// the same settings.
package collect

import (
	"os"
	"strings"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/checks/code"
	cfgchecks "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/checks/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/checks/metrics"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/cloudauth"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/secrets"
)

// Sources say where the checks read from.
type Sources struct {
	// APIKey reads Namespace settings through the Cloud Ops API. It's the
	// key the connection uses; empty skips those checks.
	APIKey string
	// Cloud is false for a dev server or self-hosted cluster, which has no
	// Cloud Ops API.
	Cloud bool
	// Repos are the user's repos, for code checks and SDK versions.
	Repos []string
	// MetricsKey is an OpenMetrics API key; empty skips the metrics checks.
	MetricsKey    string
	PrometheusURL string
}

// Resolve gathers sources for a connected target from flags, the
// environment (TCOPT_METRICS_API_KEY), the keychain, and saved settings.
// Repos from flags replace the saved ones.
func Resolve(t *config.Target, repos []string) Sources {
	s := Sources{APIKey: t.APIKey, Cloud: IsCloud(t.Address), Repos: repos}
	st, _ := config.LoadSettings()
	if st != nil {
		if len(s.Repos) == 0 {
			s.Repos = st.Repos
		}
		s.PrometheusURL = st.Metrics.PrometheusURL
	}
	s.MetricsKey = os.Getenv("TCOPT_METRICS_API_KEY")
	if s.MetricsKey == "" {
		s.MetricsKey, _ = secrets.Get(config.MetricsKeyName)
	}
	return s
}

// IsCloud reports whether an address is a Temporal Cloud endpoint.
func IsCloud(address string) bool {
	host := strings.ToLower(strings.Split(address, ":")[0])
	return strings.HasSuffix(host, ".tmprl.cloud") || strings.HasSuffix(host, ".api.temporal.io")
}

// Collectors builds the checks the sources allow. Notes say which checks
// were left out and why, for the report's caveats.
func Collectors(s Sources) (cs []analyze.Collector, notes []string) {
	cfgOpts := cfgchecks.Options{}
	// Without Cloud or a key, the config collector says itself which
	// checks didn't run.
	if s.Cloud && s.APIKey != "" {
		if ops, err := cloudauth.NewOpsWithAPIKey(s.APIKey); err == nil {
			cfgOpts.Cloud = cfgchecks.OpsSource{Ops: ops}
		} else {
			notes = append(notes, "Couldn't open the Cloud Ops API: "+err.Error())
		}
	}
	cs = append(cs, cfgchecks.New(cfgOpts))

	if s.MetricsKey != "" {
		var opts []metrics.Option
		if s.PrometheusURL != "" {
			opts = append(opts, metrics.WithPrometheus(s.PrometheusURL, ""))
		}
		cs = append(cs, metrics.New("", s.MetricsKey, opts...))
	} else if s.Cloud {
		notes = append(notes, "Cloud metrics checks need an OpenMetrics API key from a Service Account with the Metrics Read-Only role.")
	}

	if len(s.Repos) > 0 {
		cs = append(cs, code.New(s.Repos))
	} else {
		notes = append(notes, "Code checks need a repo path.")
	}
	return cs, notes
}
