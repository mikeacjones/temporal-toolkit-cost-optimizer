package features

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

// SDK is one Temporal SDK found in a repo.
type SDK struct {
	SDK     string // go, java, python, typescript, dotnet, ruby
	Version string // without a leading v; empty when only a range was found
	// Source is the file it came from, relative to the repo root.
	Source string
	// Pinned is false when the file states a range, such as >=1.30.0, and
	// Version is its lower bound.
	Pinned bool
}

// skipDirs are never searched for lockfiles.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"build": true, "target": true, "dist": true, "bin": true, "obj": true, ".gradle": true,
}

// Detect walks a repo and reads the Temporal SDK versions from its
// lockfiles and manifests. A lockfile wins over its manifest in the same
// directory, such as package-lock.json over package.json.
func Detect(root string) ([]SDK, error) {
	var out []SDK
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			found := detectDir(root, path)
			out = append(out, found...)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].SDK != out[j].SDK {
			return order(out[i].SDK) < order(out[j].SDK)
		}
		return out[i].Source < out[j].Source
	})
	return out, err
}

func detectDir(root, dir string) []SDK {
	var out []SDK
	rel := func(name string) string {
		r, err := filepath.Rel(root, filepath.Join(dir, name))
		if err != nil {
			return name
		}
		return filepath.ToSlash(r)
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil
		}
		return b
	}

	if b := read("go.mod"); b != nil {
		if v, ok := goModVersion(b); ok {
			out = append(out, SDK{SDK: Go, Version: v, Source: rel("go.mod"), Pinned: true})
		}
	}

	// Python: uv.lock pins exactly; requirements.txt and pyproject.toml may
	// state ranges.
	if b := read("uv.lock"); b != nil {
		if v, ok := uvLockVersion(b); ok {
			out = append(out, SDK{SDK: Python, Version: v, Source: rel("uv.lock"), Pinned: true})
		}
	} else if b := read("requirements.txt"); b != nil {
		if v, pinned, ok := pythonRequirement(string(b)); ok {
			out = append(out, SDK{SDK: Python, Version: v, Source: rel("requirements.txt"), Pinned: pinned})
		}
	} else if b := read("pyproject.toml"); b != nil {
		if v, pinned, ok := pythonRequirement(string(b)); ok {
			out = append(out, SDK{SDK: Python, Version: v, Source: rel("pyproject.toml"), Pinned: pinned})
		}
	}

	if b := read("package-lock.json"); b != nil {
		if v, ok := packageLockVersion(b); ok {
			out = append(out, SDK{SDK: TypeScript, Version: v, Source: rel("package-lock.json"), Pinned: true})
		}
	} else if b := read("package.json"); b != nil {
		if v, pinned, ok := packageJSONVersion(b); ok {
			out = append(out, SDK{SDK: TypeScript, Version: v, Source: rel("package.json"), Pinned: pinned})
		}
	}

	if b := read("pom.xml"); b != nil {
		if v, ok := pomVersion(string(b)); ok {
			out = append(out, SDK{SDK: Java, Version: v, Source: rel("pom.xml"), Pinned: v != ""})
		}
	}
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if b := read(name); b != nil {
			if v, ok := gradleVersion(string(b)); ok {
				out = append(out, SDK{SDK: Java, Version: v, Source: rel(name), Pinned: v != ""})
			}
		}
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".csproj") {
			continue
		}
		if v, ok := csprojVersion(string(read(e.Name()))); ok {
			out = append(out, SDK{SDK: DotNet, Version: v, Source: rel(e.Name()), Pinned: v != ""})
		}
	}
	return out
}

func goModVersion(b []byte) (string, bool) {
	f, err := modfile.ParseLax("go.mod", b, nil)
	if err != nil {
		return "", false
	}
	for _, r := range f.Require {
		if r.Mod.Path == "go.temporal.io/sdk" {
			return strings.TrimPrefix(r.Mod.Version, "v"), true
		}
	}
	return "", false
}

var uvPackage = regexp.MustCompile(`(?m)^\[\[package\]\]\s*\nname\s*=\s*"temporalio"\s*\nversion\s*=\s*"([^"]+)"`)

func uvLockVersion(b []byte) (string, bool) {
	m := uvPackage.FindSubmatch(b)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// pythonReq matches temporalio requirements such as temporalio==1.33.0,
// "temporalio>=1.30.0,<2", or temporalio[opentelemetry]~=1.20.
var pythonReq = regexp.MustCompile(`(?m)(?:^|["'\s])temporalio(?:\[[^\]]*\])?\s*(==|>=|~=|>|===)\s*([0-9][0-9A-Za-z.\-+]*)`)

func pythonRequirement(s string) (version string, pinned, ok bool) {
	m := pythonReq.FindStringSubmatch(s)
	if m == nil {
		return "", false, false
	}
	return m[2], m[1] == "==" || m[1] == "===", true
}

func packageLockVersion(b []byte) (string, bool) {
	var lock struct {
		Packages     map[string]struct{ Version string } `json:"packages"`
		Dependencies map[string]struct{ Version string } `json:"dependencies"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return "", false
	}
	for _, name := range []string{"@temporalio/client", "@temporalio/worker", "@temporalio/workflow"} {
		if p, ok := lock.Packages["node_modules/"+name]; ok && p.Version != "" {
			return p.Version, true
		}
		if p, ok := lock.Dependencies[name]; ok && p.Version != "" {
			return p.Version, true
		}
	}
	return "", false
}

var npmRange = regexp.MustCompile(`^[\^~>=v ]*([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.\-+]*)`)

func packageJSONVersion(b []byte) (string, bool, bool) {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return "", false, false
	}
	for _, name := range []string{"@temporalio/client", "@temporalio/worker", "@temporalio/workflow"} {
		spec, ok := pkg.Dependencies[name]
		if !ok {
			spec, ok = pkg.DevDependencies[name]
		}
		if !ok {
			continue
		}
		m := npmRange.FindStringSubmatch(spec)
		if m == nil {
			return "", false, true
		}
		pinned := !strings.ContainsAny(spec, "^~><")
		return m[1], pinned, true
	}
	return "", false, false
}

var (
	pomDependency = regexp.MustCompile(`(?s)<dependency>\s*(.*?)</dependency>`)
	pomArtifact   = regexp.MustCompile(`<artifactId>\s*temporal-sdk\s*</artifactId>`)
	pomGroup      = regexp.MustCompile(`<groupId>\s*io\.temporal\s*</groupId>`)
	pomVersionTag = regexp.MustCompile(`<version>\s*([^<\s]+)\s*</version>`)
	pomProperty   = regexp.MustCompile(`^\$\{([^}]+)\}$`)
)

// pomVersion finds io.temporal:temporal-sdk and resolves a ${property}
// version from <properties>. It reports ok with an empty version when the
// SDK is present but its version can't be resolved.
func pomVersion(s string) (string, bool) {
	for _, m := range pomDependency.FindAllStringSubmatch(s, -1) {
		dep := m[1]
		if !pomArtifact.MatchString(dep) || !pomGroup.MatchString(dep) {
			continue
		}
		v := pomVersionTag.FindStringSubmatch(dep)
		if v == nil {
			return "", true // managed by a BOM or parent; not resolvable here
		}
		if p := pomProperty.FindStringSubmatch(v[1]); p != nil {
			prop := regexp.MustCompile(`<` + regexp.QuoteMeta(p[1]) + `>\s*([^<\s]+)\s*</` + regexp.QuoteMeta(p[1]) + `>`)
			if pv := prop.FindStringSubmatch(s); pv != nil {
				return pv[1], true
			}
			return "", true
		}
		return v[1], true
	}
	return "", false
}

var (
	gradleCoord = regexp.MustCompile(`["']io\.temporal:temporal-sdk:([^"']+)["']`)
	gradleVar   = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_.]*)\}?$`)
)

// gradleVersion finds io.temporal:temporal-sdk:<version>, resolving a simple
// variable defined in the same file.
func gradleVersion(s string) (string, bool) {
	m := gradleCoord.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	v := m[1]
	if vm := gradleVar.FindStringSubmatch(v); vm != nil {
		def := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(vm[1]) + `\s*=\s*["']([^"']+)["']`)
		if d := def.FindStringSubmatch(s); d != nil {
			return d[1], true
		}
		return "", true
	}
	return v, true
}

var csprojRef = regexp.MustCompile(`<PackageReference\s+Include="Temporalio"\s+Version="([^"]+)"`)

func csprojVersion(s string) (string, bool) {
	m := csprojRef.FindStringSubmatch(s)
	if m == nil {
		if strings.Contains(s, `Include="Temporalio"`) {
			return "", true
		}
		return "", false
	}
	return m[1], true
}
