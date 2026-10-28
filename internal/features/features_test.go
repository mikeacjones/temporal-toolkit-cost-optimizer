package features

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T, name string) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, "testdata", "code", "repos", name)
}

func TestCatalogLoads(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"standalone-activity", "workflow-start-delay", "standalone-nexus-operation"} {
		if _, ok := c.Get(id); !ok {
			t.Errorf("catalog is missing %s", id)
		}
	}
	if _, err := Parse([]byte("features:\n  - id: x\n    cloud: beta\n")); err == nil {
		t.Error("expected an error for an unknown stage")
	}
	if _, err := Parse([]byte("features:\n  - id: x\n    cloud: ga\n    sdks:\n      cobol: {stage: ga}\n")); err == nil {
		t.Error("expected an error for an unknown SDK")
	}
}

func TestDetect(t *testing.T) {
	cases := map[string][]SDK{
		"go-new":      {{SDK: Go, Version: "1.52.0", Source: "go.mod", Pinned: true}},
		"python-uv":   {{SDK: Python, Version: "1.33.0", Source: "uv.lock", Pinned: true}},
		"python-req":  {{SDK: Python, Version: "1.30.0", Source: "requirements.txt"}},
		"node-lock":   {{SDK: TypeScript, Version: "1.12.3", Source: "package-lock.json", Pinned: true}},
		"java-pom":    {{SDK: Java, Version: "1.39.0", Source: "pom.xml", Pinned: true}},
		"java-gradle": {{SDK: Java, Version: "1.30.1", Source: "build.gradle.kts", Pinned: true}},
		"dotnet":      {{SDK: DotNet, Version: "1.19.0", Source: "Orders.csproj", Pinned: true}},
		"none":        nil,
		"mono": {
			{SDK: Go, Version: "1.52.0", Source: "svc-a/go.mod", Pinned: true},
			{SDK: Go, Version: "1.44.0", Source: "svc-b/go.mod", Pinned: true},
		},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Detect(repo(t, name))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("got %+v, want %+v", got[i], want[i])
				}
			}
		})
	}
}

func TestStatus(t *testing.T) {
	newGo := []SDK{{SDK: Go, Version: "1.52.0", Pinned: true}}
	oldGo := []SDK{{SDK: Go, Version: "1.44.0", Pinned: true}}
	cases := []struct {
		feature  string
		detected []SDK
		want     []string
	}{
		{"standalone-activity", newGo, []string{"Available in your Go SDK (v1.52.0)"}},
		{"standalone-activity", oldGo, []string{"Needs Go SDK v1.49.0 or later; you have v1.44.0"}},
		{"standalone-activity", nil, []string{"Go SDK v1.49.0 or later", "Python SDK v1.33.0 or later"}},
		{"standalone-nexus-operation", newGo, []string{"Pre-release; not recommended for production", "Available in your Go SDK (v1.52.0); pre-release"}},
		{"standalone-activity", []SDK{{SDK: Python, Version: "1.30.0"}}, []string{"Needs Python SDK v1.33.0 or later; you have v1.30.0 or later"}},
		// The oldest of several services decides.
		{"standalone-activity", append(newGo, oldGo...), []string{"Needs Go SDK v1.49.0 or later; you have v1.44.0"}},
	}
	for _, tc := range cases {
		lines, err := Status(tc.feature, tc.detected)
		if err != nil {
			t.Fatal(err)
		}
		var msgs []string
		for _, l := range lines {
			msgs = append(msgs, l.Message)
		}
		joined := strings.Join(msgs, " | ")
		for _, w := range tc.want {
			if !strings.Contains(joined, w) {
				t.Errorf("%s with %v: got %q, want it to contain %q", tc.feature, tc.detected, joined, w)
			}
		}
	}
	if _, err := Status("no-such-feature", nil); err == nil {
		t.Error("expected an error for an unknown feature")
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.49.0", "1.49.0", 0}, {"1.44.0", "1.49.0", -1}, {"v1.52.0", "1.49.0", 1}, {"1.10.0", "1.9.0", 1}, {"1.49.0-rc.1", "1.49.0", -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
