package code

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

func fixtures(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "code")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// wants reads `// want CODE-xxx` markers: file:line for each rule.
func wants(t *testing.T, root, id string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		rel, _ := filepath.Rel(root, p)
		sc := bufio.NewScanner(f)
		for n := 1; sc.Scan(); n++ {
			if strings.Contains(sc.Text(), "// want "+id) {
				out = append(out, filepath.ToSlash(rel)+":"+itoa(n))
			}
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func itoa(n int) string {
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func got(fs []estimate.Finding, id string) []string {
	var out []string
	for _, f := range fs {
		if f.ID != id {
			continue
		}
		if f.Source != estimate.SourceCode {
			panic("code finding without the code source")
		}
		for _, e := range f.Evidence {
			out = append(out, e.File+":"+itoa(e.Line))
		}
	}
	slices.Sort(out)
	return out
}

func TestRules(t *testing.T) {
	rs, err := LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		id := "CODE-0" + map[bool]string{true: "0", false: ""}[i < 10] + itoa(i)
		if _, ok := rs[id]; !ok {
			t.Errorf("no rule file for %s", id)
		}
	}
	for id, r := range rs {
		_, hasMatcher := goMatchers[id]
		if r.GoStatus() == "implemented" && !hasMatcher && id != "CODE-008" {
			t.Errorf("%s says Go is implemented but has no matcher", id)
		}
	}
}

func TestFixtures(t *testing.T) {
	root := fixtures(t)
	for id := range goMatchers {
		t.Run(id, func(t *testing.T) {
			pos := filepath.Join(root, id, "positive")
			fs, err := Scan(context.Background(), pos)
			if err != nil {
				t.Fatal(err)
			}
			want := wants(t, pos, id)
			if len(want) == 0 {
				t.Fatal("positive fixture has no // want markers")
			}
			if g := got(fs, id); !slices.Equal(g, want) {
				t.Errorf("positive: got %v, want %v", g, want)
			}
			for _, f := range fs {
				if f.ID != id {
					t.Errorf("positive fixture also matched %s: %+v", f.ID, f.Evidence)
				}
			}

			neg := filepath.Join(root, id, "negative")
			fs, err = Scan(context.Background(), neg)
			if err != nil {
				t.Fatal(err)
			}
			if g := got(fs, id); len(g) != 0 {
				t.Errorf("negative: got %v, want nothing", g)
			}
		})
	}
}

func TestCollectorMergesRepos(t *testing.T) {
	root := fixtures(t)
	c := New([]string{filepath.Join(root, "CODE-001", "positive"), filepath.Join(root, "CODE-010", "positive")})
	if c.Name() != "Code" {
		t.Errorf("name %q", c.Name())
	}
	fs, err := c.Collect(context.Background(), &analyze.Env{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, f := range fs {
		ids[f.ID] = true
		for _, e := range f.Evidence {
			if !strings.HasPrefix(e.File, "positive/") {
				t.Errorf("evidence path %q should carry the repo prefix", e.File)
			}
		}
	}
	if !ids["CODE-001"] || !ids["CODE-010"] {
		t.Errorf("got %v, want CODE-001 and CODE-010", ids)
	}
	if _, err := New([]string{"/no/such/repo"}).Collect(context.Background(), &analyze.Env{}); err == nil {
		t.Error("expected an error for a missing repo")
	}
}

func TestKnownIssuesNeedConfirmation(t *testing.T) {
	rule := Rule{ID: "CODE-008", KnownIssues: []KnownIssue{
		{ID: "unconfirmed", SDKs: []string{"go"}, FixedIn: map[string]string{"go": "9.9.9"}},
		{ID: "confirmed", SDKs: []string{"go"}, FixedIn: map[string]string{"go": "1.50.0"}, Confirmed: true, Description: "A test bug."},
	}}
	repo := filepath.Join(fixtures(t), "repos", "mono")
	hs, err := knownIssueHits(repo, rule)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].file != "svc-b/go.mod" || hs[0].keys[0] != "confirmed" {
		t.Errorf("got %+v, want one hit on svc-b/go.mod (v1.44.0) for the confirmed issue", hs)
	}
	// The shipped rule has no confirmed versions yet, so it finds nothing.
	rs, _ := LoadRules()
	if hs, _ := knownIssueHits(repo, rs["CODE-008"]); len(hs) != 0 {
		t.Errorf("shipped CODE-008 should be inert until versions are confirmed, got %+v", hs)
	}
}

// TestScanRepo scans a real repo named by TCOPT_SCAN_REPO and prints the
// findings, as a manual check that the rules hold up outside fixtures.
func TestScanRepo(t *testing.T) {
	root := os.Getenv("TCOPT_SCAN_REPO")
	if root == "" {
		t.Skip("set TCOPT_SCAN_REPO to scan a repo")
	}
	fs, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		for _, e := range f.Evidence {
			t.Logf("%s %s:%d %s", f.ID, e.File, e.Line, e.Note)
		}
	}
}
