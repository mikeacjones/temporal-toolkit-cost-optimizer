package config

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/secrets"
)

// Target sources.
const (
	// SourceCloudLogin is a Namespace reached with an API key that tcopt
	// created after a browser login.
	SourceCloudLogin = "cloud-login"
	// SourceProfile is a profile from the Temporal CLI's temporal.toml.
	SourceProfile = "profile"
)

// Settings are tcopt's own settings. Secrets live in the keychain, never
// here.
type Settings struct {
	Target TargetSettings `toml:"target"`
	// Repos are the user's repos, for reading SDK versions.
	Repos []string `toml:"repos,omitempty"`
}

// ResolveRepos returns repos, or the saved repos when it's empty.
func ResolveRepos(repos []string) []string {
	if len(repos) > 0 {
		return repos
	}
	if st, err := LoadSettings(); err == nil {
		return st.Repos
	}
	return nil
}

// TargetSettings remember which Namespace to analyze and how to reach it.
type TargetSettings struct {
	Source     string    `toml:"source"`
	Profile    string    `toml:"profile,omitempty"`
	ConfigFile string    `toml:"config_file,omitempty"`
	Namespace  string    `toml:"namespace"`
	Address    string    `toml:"address,omitempty"`
	Email      string    `toml:"email,omitempty"`
	KeyID      string    `toml:"key_id,omitempty"`
	KeyName    string    `toml:"key_name,omitempty"`
	KeyExpires time.Time `toml:"key_expires,omitempty"`
}

// KeyExpired reports whether the key tcopt created has expired, or will
// within a minute.
func (t TargetSettings) KeyExpired() bool {
	return !t.KeyExpires.IsZero() && time.Now().Add(time.Minute).After(t.KeyExpires)
}

// SettingsPath is ~/.config/tcopt/config.toml, or under $XDG_CONFIG_HOME.
func SettingsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tcopt", "config.toml")
}

// LoadSettings reads the settings file. A missing file gives empty settings.
func LoadSettings() (*Settings, error) {
	s := &Settings{}
	_, err := toml.DecodeFile(SettingsPath(), s)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	return s, err
}

// Save writes the settings file.
func (s *Settings) Save() error {
	path := SettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(s)
}

// NamespaceKeyName is the keychain entry for a Namespace's API key.
func NamespaceKeyName(namespace string) string { return "namespace-api-key:" + namespace }

// ErrKeyMissing means the saved target needs an API key that the keychain
// doesn't have, or that has expired.
var ErrKeyMissing = errors.New("the API key for this Namespace is missing or expired")

// DialSaved connects to the saved target.
func DialSaved(t TargetSettings) (*Target, error) {
	switch t.Source {
	case SourceProfile:
		return DialProfile(t.Profile, t.ConfigFile, t.Namespace)
	case SourceCloudLogin:
		if t.KeyExpired() {
			return nil, ErrKeyMissing
		}
		key, err := secrets.Get(NamespaceKeyName(t.Namespace))
		if errors.Is(err, secrets.ErrNotFound) {
			return nil, ErrKeyMissing
		}
		if err != nil {
			return nil, err
		}
		return DialAPIKey(t.Address, t.Namespace, key, "cloud login")
	}
	return nil, fmt.Errorf("unknown target source %q", t.Source)
}

// DialAPIKey connects to a Namespace endpoint with an API key over TLS.
func DialAPIKey(address, namespace, key, label string) (*Target, error) {
	c, err := client.Dial(client.Options{
		HostPort:          address,
		Namespace:         namespace,
		Credentials:       client.NewAPIKeyStaticCredentials(key),
		ConnectionOptions: client.ConnectionOptions{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
		Logger:            quiet{},
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", address, err)
	}
	return &Target{Profile: label, Address: address, Namespace: namespace, Client: c}, nil
}

// ProfileInfo describes one profile in temporal.toml, without secrets.
type ProfileInfo struct {
	Name      string
	Address   string
	Namespace string
}

// Profiles lists the profiles in the Temporal CLI's temporal.toml.
func Profiles(configFile string) ([]ProfileInfo, error) {
	cfg, err := envconfig.LoadClientConfig(envconfig.LoadClientConfigOptions{ConfigFilePath: configFile})
	if err != nil {
		return nil, err
	}
	var out []ProfileInfo
	for name, p := range cfg.Profiles {
		out = append(out, ProfileInfo{Name: name, Address: p.Address, Namespace: p.Namespace})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Explicit reports whether the user chose a target on the command line or
// in the environment, which overrides the saved one.
func Explicit(profile string) bool {
	return profile != "" || os.Getenv("TEMPORAL_PROFILE") != "" || os.Getenv("TEMPORAL_ADDRESS") != ""
}

// CheckRead confirms the target can list executions, which is what tcopt
// needs. A new API key can authenticate before its permissions reach the
// Namespace, so a successful Dial isn't enough.
func CheckRead(ctx context.Context, t *Target) error {
	_, err := t.Client.WorkflowService().ListWorkflowExecutions(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: t.Namespace, PageSize: 1,
	})
	return err
}

// IsAuthError reports whether err means the credentials were rejected or
// lack permission, as opposed to a network or server problem.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	var se interface{ GRPCStatus() *status.Status }
	if errors.As(err, &se) {
		switch se.GRPCStatus().Code() {
		case codes.Unauthenticated, codes.PermissionDenied:
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthenticated") || strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "permissiondenied") || strings.Contains(msg, "jwt is expired")
}

// SaveTarget replaces the saved target and keeps the other settings.
func SaveTarget(t TargetSettings) error {
	st, err := LoadSettings()
	if err != nil {
		st = &Settings{}
	}
	st.Target = t
	return st.Save()
}
