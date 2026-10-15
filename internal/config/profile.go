// Package config connects to Temporal with the user's existing CLI profiles.
package config

import (
	"errors"
	"fmt"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

// ErrNoProfile is returned when neither a profile nor TEMPORAL_ADDRESS was
// given. tcopt never falls back to the default profile on its own, because
// it may point at production.
var ErrNoProfile = errors.New("name a profile with --profile or TEMPORAL_PROFILE, or set TEMPORAL_ADDRESS; tcopt doesn't fall back to the default profile")

// EnvProfile is the label for a connection configured only by TEMPORAL_*
// environment variables.
const EnvProfile = "env"

// Target is a connection to the Namespace being analyzed.
type Target struct {
	Profile   string
	Address   string
	Namespace string
	Client    client.Client
	// APIKey is the key the connection uses, when it uses one. The config
	// checks reuse it to read Namespace settings from the Cloud Ops API.
	APIKey string
}

// Close closes the client.
func (t *Target) Close() {
	if t.Client != nil {
		t.Client.Close()
	}
}

// DialProfile connects the way the Temporal CLI does. With a profile, named
// by the argument or TEMPORAL_PROFILE, it reads that profile from
// temporal.toml and applies TEMPORAL_* environment variables on top. Without
// one, it connects from the environment variables alone, and requires
// TEMPORAL_ADDRESS so it never lands on a default by accident. configFile
// overrides the file path, and namespace overrides the Namespace.
func DialProfile(profile, configFile, namespace string) (*Target, error) {
	if profile == "" {
		profile = os.Getenv("TEMPORAL_PROFILE")
	}
	req := envconfig.LoadClientConfigProfileOptions{ConfigFilePath: configFile, ConfigFileProfile: profile}
	label := profile
	if profile == "" {
		if os.Getenv("TEMPORAL_ADDRESS") == "" {
			return nil, ErrNoProfile
		}
		req = envconfig.LoadClientConfigProfileOptions{DisableFile: true}
		label = EnvProfile
	}
	prof, err := envconfig.LoadClientConfigProfile(req)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", label, err)
	}
	opts, err := prof.ToClientOptions(envconfig.ToClientOptionsRequest{})
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", label, err)
	}
	if namespace != "" {
		opts.Namespace = namespace
	}
	if opts.Namespace == "" {
		opts.Namespace = client.DefaultNamespace
	}
	if opts.HostPort == "" {
		opts.HostPort = client.DefaultHostPort
	}
	opts.Logger = quiet{}
	c, err := client.Dial(opts)
	if err != nil {
		return nil, fmt.Errorf("connect to %s with profile %q: %w", opts.HostPort, label, err)
	}
	return &Target{Profile: label, Address: opts.HostPort, Namespace: opts.Namespace, Client: c, APIKey: prof.APIKey}, nil
}

// quiet drops SDK log lines so they don't draw over the TUI or mix into
// report output.
type quiet struct{}

func (quiet) Debug(string, ...any) {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}
func (quiet) Error(string, ...any) {}
