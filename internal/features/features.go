// Package features reads the feature catalog (kb/features.yaml): newer
// Temporal features a fix may recommend, with their release stage in Cloud
// and in each SDK. It also detects which Temporal SDKs a repo uses, from its
// lockfiles, so findings can say whether the user can use a feature today.
package features

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	kbdata "github.com/mikeacjones/temporal-toolkit-cost-optimizer/kb"
)

// Release stages.
const (
	StageGA            = "ga"
	StagePublicPreview = "public-preview"
	StagePreRelease    = "pre-release"
	StageUnknown       = "unknown"
)

// SDK names, as the catalog and Detect use them.
const (
	Go         = "go"
	Java       = "java"
	Python     = "python"
	TypeScript = "typescript"
	DotNet     = "dotnet"
	Ruby       = "ruby"
)

// sdkNames are the display names for messages.
var sdkNames = map[string]string{
	Go: "Go", Java: "Java", Python: "Python", TypeScript: "TypeScript", DotNet: ".NET", Ruby: "Ruby",
}

// sdkOrder is the order messages list SDKs in.
var sdkOrder = []string{Go, Java, Python, TypeScript, DotNet, Ruby}

// Support is one SDK's support for a feature.
type Support struct {
	Stage string `yaml:"stage"`
	// Min is the first version at Stage, or the first version with the API
	// when Stage is unknown. Empty when not confirmed.
	Min string `yaml:"min"`
}

// Feature is one catalog entry.
type Feature struct {
	ID     string             `yaml:"id"`
	Title  string             `yaml:"title"`
	Docs   string             `yaml:"docs"`
	Cloud  string             `yaml:"cloud"`
	SDKs   map[string]Support `yaml:"sdks"`
	Saves  string             `yaml:"saves"`
	UsedBy []string           `yaml:"used_by"`
}

// PreRelease reports whether the feature is pre-release in Cloud. Findings
// whose only fix is a pre-release feature are hidden unless the user asks
// for previews.
func (f *Feature) PreRelease() bool { return f.Cloud == StagePreRelease }

// Catalog is the parsed feature catalog.
type Catalog struct {
	Snapshot string    `yaml:"snapshot"`
	Features []Feature `yaml:"features"`
}

var (
	loadOnce sync.Once
	catalog  *Catalog
	loadErr  error
)

// Load returns the embedded catalog.
func Load() (*Catalog, error) {
	loadOnce.Do(func() {
		b, err := fs.ReadFile(kbdata.FS, "features.yaml")
		if err != nil {
			loadErr = err
			return
		}
		catalog, loadErr = Parse(b)
	})
	return catalog, loadErr
}

// Parse parses a catalog and checks its stages and versions.
func Parse(b []byte) (*Catalog, error) {
	c := &Catalog{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, f := range c.Features {
		if seen[f.ID] {
			return nil, fmt.Errorf("feature %s listed twice", f.ID)
		}
		seen[f.ID] = true
		if !validStage(f.Cloud) {
			return nil, fmt.Errorf("feature %s: unknown Cloud stage %q", f.ID, f.Cloud)
		}
		for sdk, s := range f.SDKs {
			if _, ok := sdkNames[sdk]; !ok {
				return nil, fmt.Errorf("feature %s: unknown SDK %q", f.ID, sdk)
			}
			if !validStage(s.Stage) {
				return nil, fmt.Errorf("feature %s: %s: unknown stage %q", f.ID, sdk, s.Stage)
			}
			if s.Min != "" && !semver.IsValid(canonical(s.Min)) {
				return nil, fmt.Errorf("feature %s: %s: bad version %q", f.ID, sdk, s.Min)
			}
		}
	}
	return c, nil
}

func validStage(s string) bool {
	switch s {
	case StageGA, StagePublicPreview, StagePreRelease, StageUnknown:
		return true
	}
	return false
}

// Get returns a feature by ID.
func (c *Catalog) Get(id string) (*Feature, bool) {
	for i := range c.Features {
		if c.Features[i].ID == id {
			return &c.Features[i], true
		}
	}
	return nil, false
}

// Line is one status message about a feature, for one SDK or for the
// feature as a whole.
type Line struct {
	SDK     string // empty for a message about the whole feature
	Message string
	// Usable is true when a detected SDK has the feature at GA or public
	// preview.
	Usable bool
}

// Status describes whether the user can use a feature, as PLAN.md 6.11
// words it. With detected SDKs it says, for each, whether that version has
// the feature. With none, it lists each SDK's minimum version.
func Status(featureID string, detected []SDK) ([]Line, error) {
	c, err := Load()
	if err != nil {
		return nil, err
	}
	return c.Status(featureID, detected)
}

// Status is the method form of the package-level Status.
func (c *Catalog) Status(featureID string, detected []SDK) ([]Line, error) {
	f, ok := c.Get(featureID)
	if !ok {
		return nil, fmt.Errorf("no feature %q in the catalog", featureID)
	}
	var out []Line
	if f.PreRelease() {
		out = append(out, Line{Message: "Pre-release; not recommended for production"})
	}

	if len(detected) == 0 {
		for _, sdk := range sdkOrder {
			s, ok := f.SDKs[sdk]
			if !ok {
				continue
			}
			out = append(out, Line{SDK: sdk, Message: minimumMessage(sdk, s)})
		}
		return out, nil
	}

	for _, d := range dedupe(detected) {
		name := sdkNames[d.SDK]
		s, ok := f.SDKs[d.SDK]
		switch {
		case !ok || (s.Min == "" && s.Stage == StageUnknown):
			out = append(out, Line{SDK: d.SDK, Message: fmt.Sprintf("Not confirmed for the %s SDK; you have %s", name, have(d))})
		case s.Min == "":
			out = append(out, Line{SDK: d.SDK, Message: fmt.Sprintf("%s in the %s SDK, minimum version not confirmed; you have %s", capitalize(stageWords(s.Stage)), name, have(d))})
		case d.Version == "":
			out = append(out, Line{SDK: d.SDK, Message: fmt.Sprintf("Needs %s SDK v%s or later; couldn't read your version from %s", name, s.Min, d.Source)})
		case Compare(d.Version, s.Min) < 0:
			out = append(out, Line{SDK: d.SDK, Message: fmt.Sprintf("Needs %s SDK v%s or later; you have %s", name, s.Min, have(d))})
		default:
			msg := fmt.Sprintf("Available in your %s SDK (%s)", name, have(d))
			usable := s.Stage == StageGA || s.Stage == StagePublicPreview
			switch s.Stage {
			case StagePublicPreview:
				msg += "; public preview"
			case StagePreRelease:
				msg += "; pre-release"
			case StageUnknown:
				msg += "; release stage not confirmed"
				usable = true
			}
			out = append(out, Line{SDK: d.SDK, Message: msg, Usable: usable})
		}
	}
	return out, nil
}

// have describes a detected version: "v1.52.0", or "v1.30.0 or later" for
// a range.
func have(d SDK) string {
	if d.Version == "" {
		return "an unknown version"
	}
	if !d.Pinned {
		return "v" + d.Version + " or later"
	}
	return "v" + d.Version
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func minimumMessage(sdk string, s Support) string {
	name := sdkNames[sdk]
	switch {
	case s.Min != "" && s.Stage == StageGA:
		return fmt.Sprintf("%s SDK v%s or later", name, s.Min)
	case s.Min != "" && s.Stage == StageUnknown:
		return fmt.Sprintf("%s SDK v%s or later; release stage not confirmed", name, s.Min)
	case s.Min != "":
		return fmt.Sprintf("%s SDK v%s or later; %s", name, s.Min, stageWords(s.Stage))
	case s.Stage == StageUnknown:
		return fmt.Sprintf("%s SDK: not confirmed", name)
	}
	return fmt.Sprintf("%s SDK: %s; minimum version not confirmed", name, stageWords(s.Stage))
}

func stageWords(stage string) string {
	switch stage {
	case StageGA:
		return "generally available"
	case StagePublicPreview:
		return "public preview"
	case StagePreRelease:
		return "pre-release"
	}
	return "release stage not confirmed"
}

// dedupe keeps one entry per SDK: the lowest version found, since a repo
// with several services is only as able as its oldest one.
func dedupe(ds []SDK) []SDK {
	best := map[string]SDK{}
	for _, d := range ds {
		cur, ok := best[d.SDK]
		if !ok || (d.Version != "" && (cur.Version == "" || Compare(d.Version, cur.Version) < 0)) {
			best[d.SDK] = d
		}
	}
	out := make([]SDK, 0, len(best))
	for _, d := range best {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return order(out[i].SDK) < order(out[j].SDK) })
	return out
}

func order(sdk string) int {
	for i, s := range sdkOrder {
		if s == sdk {
			return i
		}
	}
	return len(sdkOrder)
}

// Compare compares two versions such as "1.49.0" or "v1.49.0-rc.1". It
// returns -1, 0, or 1. Unparseable versions compare as equal.
func Compare(a, b string) int {
	ca, cb := canonical(a), canonical(b)
	if !semver.IsValid(ca) || !semver.IsValid(cb) {
		return 0
	}
	return semver.Compare(ca, cb)
}

// canonical turns "1.2.3" into "v1.2.3" and drops build metadata.
func canonical(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}
