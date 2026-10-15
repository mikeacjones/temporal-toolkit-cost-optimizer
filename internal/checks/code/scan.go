package code

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/features"
)

// MaxEvidence caps how many locations one code finding cites.
const MaxEvidence = 20

// Import paths the matchers recognize.
const (
	pkgClient    = "go.temporal.io/sdk/client"
	pkgWorkflow  = "go.temporal.io/sdk/workflow"
	pkgActivity  = "go.temporal.io/sdk/activity"
	pkgConverter = "go.temporal.io/sdk/converter"
	pkgHTTP      = "net/http"
	pkgTime      = "time"
)

// skipDirs are never scanned.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"build": true, "target": true, "dist": true, "testdata": true,
}

// file is one parsed Go file.
type file struct {
	path    string // relative to the scan root, slash-separated
	ast     *ast.File
	fset    *token.FileSet
	imports map[string]string // local name -> import path
	pkgDir  string
}

// has reports whether the file imports path.
func (f *file) has(path string) bool {
	for _, p := range f.imports {
		if p == path {
			return true
		}
	}
	return false
}

// pkgFunc reports whether call is <alias>.<name>(...) where alias imports
// path, and returns name.
func (f *file) pkgFunc(call *ast.CallExpr, path string) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || f.imports[id.Name] != path {
		return "", false
	}
	return sel.Sel.Name, true
}

// isPkgType reports whether expr is <alias>.<name> for an alias of path.
func (f *file) isPkgType(expr ast.Expr, path, name string) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && f.imports[id.Name] == path
}

// method returns the selector name of a call such as c.QueryWorkflow(...).
// Without type information it can't tell a method from a package function,
// so callers only ask about names no SDK package exports as functions.
func (f *file) method(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	return sel.Sel.Name, true
}

func (f *file) line(n ast.Node) int { return f.fset.Position(n.Pos()).Line }

// hit is one match of a rule.
type hit struct {
	id   string
	file string
	line int
	note string
	keys []string
}

// repo is a parsed set of Go files with an index of top-level functions and
// methods by name, for resolving handlers and Activities.
type repo struct {
	files []*file
	funcs map[string][]funcRef // name -> declarations
}

type funcRef struct {
	f    *file
	decl *ast.FuncDecl
}

func parseRepo(ctx context.Context, root string) (*repo, error) {
	r := &repo{funcs: map[string][]funcRef{}}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		af, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // a file that doesn't parse is skipped
		}
		rel, _ := filepath.Rel(root, p)
		fl := &file{path: filepath.ToSlash(rel), ast: af, fset: fset, imports: imports(af), pkgDir: filepath.Dir(rel)}
		r.files = append(r.files, fl)
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				r.funcs[fd.Name.Name] = append(r.funcs[fd.Name.Name], funcRef{fl, fd})
			}
		}
		return nil
	})
	return r, err
}

// imports maps each import's local name to its path. A dot or blank import
// is left out.
func imports(af *ast.File) map[string]string {
	out := map[string]string{}
	for _, im := range af.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		out[name] = p
	}
	return out
}

// samePackage returns declarations named name in the same directory as f.
func (r *repo) samePackage(f *file, name string) []funcRef {
	var out []funcRef
	for _, ref := range r.funcs[name] {
		if ref.f.pkgDir == f.pkgDir {
			out = append(out, ref)
		}
	}
	return out
}

// walk visits every node with the stack of its ancestors, innermost last.
func walk(root ast.Node, fn func(n ast.Node, stack []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		fn(n, stack)
		stack = append(stack, n)
		return true
	})
}

// enclosingFunc returns the innermost function in the stack: its type and
// body.
func enclosingFunc(stack []ast.Node) (*ast.FuncType, *ast.BlockStmt) {
	for i := len(stack) - 1; i >= 0; i-- {
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			return fn.Type, fn.Body
		case *ast.FuncDecl:
			return fn.Type, fn.Body
		}
	}
	return nil, nil
}

// exprString renders an expression as source-like text for comparisons.
func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.BasicLit:
		return x.Value
	case *ast.CallExpr:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = exprString(a)
		}
		return exprString(x.Fun) + "(" + strings.Join(args, ", ") + ")"
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.UnaryExpr:
		return x.Op.String() + exprString(x.X)
	case *ast.BinaryExpr:
		return exprString(x.X) + " " + x.Op.String() + " " + exprString(x.Y)
	case *ast.ParenExpr:
		return "(" + exprString(x.X) + ")"
	case *ast.IndexExpr:
		return exprString(x.X) + "[" + exprString(x.Index) + "]"
	case *ast.CompositeLit:
		return exprString(x.Type) + "{...}"
	}
	return fmt.Sprintf("%T", e)
}

// field returns the value of key in a composite literal, if set.
func field(lit *ast.CompositeLit, key string) (ast.Expr, bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == key {
			return kv.Value, true
		}
	}
	return nil, false
}

// literalString returns the value of a string literal, or "".
func literalString(e ast.Expr) string {
	if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		s, err := strconv.Unquote(bl.Value)
		if err == nil {
			return s
		}
	}
	return ""
}

// localLiteral resolves an identifier to the composite literal it was last
// assigned before pos in body, such as `opts := client.StartWorkflowOptions{...}`.
func localLiteral(body *ast.BlockStmt, name string, pos token.Pos) *ast.CompositeLit {
	var found *ast.CompositeLit
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil || n.Pos() >= pos {
			return false
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name && i < len(s.Rhs) {
					if lit := asLiteral(s.Rhs[i]); lit != nil {
						found = lit
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range s.Names {
				if id.Name == name && i < len(s.Values) {
					if lit := asLiteral(s.Values[i]); lit != nil {
						found = lit
					}
				}
			}
		}
		return true
	})
	return found
}

func asLiteral(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}

// Scan runs every implemented Go rule over the repo at root and returns one
// finding per rule that matched. File paths in evidence are relative to
// root.
func Scan(ctx context.Context, root string) ([]estimate.Finding, error) {
	rs, err := LoadRules()
	if err != nil {
		return nil, err
	}
	r, err := parseRepo(ctx, root)
	if err != nil {
		return nil, err
	}
	var hits []hit
	for _, id := range RuleIDs() {
		m, ok := goMatchers[id]
		if !ok || rs[id].GoStatus() != "implemented" {
			continue
		}
		hits = append(hits, m(r, rs[id])...)
	}
	sdkHits, err := knownIssueHits(root, rs["CODE-008"])
	if err != nil {
		return nil, err
	}
	hits = append(hits, sdkHits...)
	return findings(hits, rs), nil
}

// knownIssueHits implements CODE-008: SDK versions on the known-issues list.
// Issues without confirmed affected versions produce nothing.
func knownIssueHits(root string, rule Rule) ([]hit, error) {
	var confirmed []KnownIssue
	for _, ki := range rule.KnownIssues {
		if ki.Confirmed && len(ki.FixedIn) > 0 {
			confirmed = append(confirmed, ki)
		}
	}
	if len(confirmed) == 0 {
		return nil, nil
	}
	sdks, err := features.Detect(root)
	if err != nil {
		return nil, err
	}
	var out []hit
	for _, s := range sdks {
		for _, ki := range confirmed {
			fixed, ok := ki.FixedIn[s.SDK]
			if !ok || s.Version == "" || features.Compare(s.Version, fixed) >= 0 {
				continue
			}
			out = append(out, hit{
				id: rule.ID, file: s.Source,
				note: fmt.Sprintf("%s SDK v%s: %s Fixed in v%s.", s.SDK, s.Version, strings.TrimSpace(ki.Description), fixed),
				keys: []string{ki.ID},
			})
		}
	}
	return out, nil
}

// findings groups hits by rule.
func findings(hits []hit, rs map[string]Rule) []estimate.Finding {
	by := map[string][]hit{}
	for _, h := range hits {
		by[h.id] = append(by[h.id], h)
	}
	ids := make([]string, 0, len(by))
	for id := range by {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []estimate.Finding
	for _, id := range ids {
		hs := by[id]
		sort.Slice(hs, func(i, j int) bool {
			if hs[i].file != hs[j].file {
				return hs[i].file < hs[j].file
			}
			return hs[i].line < hs[j].line
		})
		rule := rs[id]
		f := estimate.Finding{
			ID: id, Title: rule.Title, Source: estimate.SourceCode,
			Confidence: detect.Confidence(rule.Confidence), Guidance: id, Features: rule.Features,
		}
		keys := map[string]bool{}
		for i, h := range hs {
			if i < MaxEvidence {
				f.Evidence = append(f.Evidence, estimate.Evidence{File: h.file, Line: h.line, Note: h.note})
			}
			for _, k := range h.keys {
				keys[k] = true
			}
		}
		if len(hs) > MaxEvidence {
			f.Evidence = append(f.Evidence, estimate.Evidence{Note: fmt.Sprintf("and %d more places", len(hs)-MaxEvidence)})
		}
		for k := range keys {
			f.Keys = append(f.Keys, k)
		}
		sort.Strings(f.Keys)
		out = append(out, f)
	}
	return out
}
