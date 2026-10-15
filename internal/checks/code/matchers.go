package code

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"time"
)

// goMatchers holds the Go implementation of each rule. CODE-006 is Java
// only, and CODE-008 reads lockfiles instead of source.
var goMatchers = map[string]func(r *repo, rule Rule) []hit{
	"CODE-001": queryPolling,
	"CODE-002": handlerTimeoutWaits,
	"CODE-003": heartbeatSettings,
	"CODE-004": encryptWithoutCompression,
	"CODE-005": scheduleCronUpdate,
	"CODE-007": startThenMessage,
	"CODE-009": externalCallsWithoutRetryLimit,
	"CODE-010": queryReturnsVisibleState,
}

// queryPolling finds Workflow Queries sent from a loop or an HTTP handler.
func queryPolling(r *repo, rule Rule) []hit {
	var out []hit
	for _, f := range r.files {
		if !f.has(pkgClient) {
			continue
		}
		walk(f.ast, func(n ast.Node, stack []ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			name, _ := f.method(call)
			if name != "QueryWorkflow" && name != "QueryWorkflowWithOptions" {
				return
			}
			where := ""
			switch {
			case inLoop(stack):
				where = "inside a loop"
			case inHTTPHandler(f, stack):
				where = "inside an HTTP handler"
			default:
				return
			}
			h := hit{id: rule.ID, file: f.path, line: f.line(call), note: fmt.Sprintf("%s %s; each Query is one Action", name, where)}
			if qt := queryType(name, call); qt != "" {
				h.keys = []string{qt}
				h.note = fmt.Sprintf("%s %q %s; each Query is one Action", name, qt, where)
			}
			out = append(out, h)
		})
	}
	return out
}

func queryType(method string, call *ast.CallExpr) string {
	if method == "QueryWorkflow" && len(call.Args) >= 4 {
		return literalString(call.Args[3])
	}
	if method == "QueryWorkflowWithOptions" && len(call.Args) >= 2 {
		if lit := asLiteral(call.Args[1]); lit != nil {
			if v, ok := field(lit, "QueryType"); ok {
				return literalString(v)
			}
		}
	}
	return ""
}

// inLoop reports whether the innermost function in the stack has a for or
// range statement around the node. Loops outside a function literal don't
// count, because the literal may run once.
func inLoop(stack []ast.Node) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i].(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return true
		case *ast.FuncLit, *ast.FuncDecl:
			return false
		}
	}
	return false
}

// inHTTPHandler reports whether the innermost function takes an
// http.ResponseWriter and an *http.Request, or is a literal passed to
// HandleFunc or http.HandlerFunc.
func inHTTPHandler(f *file, stack []ast.Node) bool {
	if !f.has(pkgHTTP) {
		return false
	}
	for i := len(stack) - 1; i >= 0; i-- {
		var ft *ast.FuncType
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			ft = fn.Type
			if i > 0 {
				if call, ok := stack[i-1].(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "HandleFunc" || sel.Sel.Name == "HandlerFunc") {
						return true
					}
				}
			}
		case *ast.FuncDecl:
			ft = fn.Type
		default:
			continue
		}
		var w, req bool
		for _, p := range ft.Params.List {
			w = w || f.isPkgType(p.Type, pkgHTTP, "ResponseWriter")
			if star, ok := p.Type.(*ast.StarExpr); ok {
				req = req || f.isPkgType(star.X, pkgHTTP, "Request")
			}
		}
		return w && req
	}
	return false
}

// handlerTimeoutWaits finds waits with a timeout inside Update and Signal
// handlers.
func handlerTimeoutWaits(r *repo, rule Rule) []hit {
	var out []hit
	seen := map[token.Pos]bool{}
	for _, f := range r.files {
		if !f.has(pkgWorkflow) {
			continue
		}
		walk(f.ast, func(n ast.Node, _ []ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			var handler ast.Expr
			var what string
			if name, ok := f.pkgFunc(call, pkgWorkflow); ok && (name == "SetUpdateHandler" || name == "SetUpdateHandlerWithOptions") && len(call.Args) >= 3 {
				handler = call.Args[2]
				what = "Update handler"
				if u := literalString(call.Args[1]); u != "" {
					what = fmt.Sprintf("Update handler %q", u)
				}
			} else if m, _ := f.method(call); m == "AddReceive" && len(call.Args) >= 2 {
				handler = call.Args[1]
				what = "Signal handler"
			} else {
				return
			}
			for _, body := range resolveHandler(r, f, handler) {
				for _, w := range timeoutWaits(body.f, body.body) {
					if seen[w.Pos()] {
						continue
					}
					seen[w.Pos()] = true
					out = append(out, hit{
						id: rule.ID, file: body.f.path, line: body.f.line(w),
						note: fmt.Sprintf("wait with a timeout inside the %s starts a Timer, one Action, on every call", what),
					})
				}
			}
		})
	}
	return out
}

type handlerBody struct {
	f    *file
	body *ast.BlockStmt
}

// resolveHandler returns the body of a handler given as a function literal,
// a function name, or a method value declared in the same package.
func resolveHandler(r *repo, f *file, e ast.Expr) []handlerBody {
	switch h := e.(type) {
	case *ast.FuncLit:
		return []handlerBody{{f, h.Body}}
	case *ast.Ident:
		var out []handlerBody
		for _, ref := range r.samePackage(f, h.Name) {
			if ref.decl.Recv == nil {
				out = append(out, handlerBody{ref.f, ref.decl.Body})
			}
		}
		return out
	case *ast.SelectorExpr:
		var out []handlerBody
		for _, ref := range r.samePackage(f, h.Sel.Name) {
			if ref.decl.Recv != nil {
				out = append(out, handlerBody{ref.f, ref.decl.Body})
			}
		}
		return out
	}
	return nil
}

// timeoutWaits finds workflow.AwaitWithTimeout calls, and AwaitWithOptions
// calls whose options set a Timeout.
func timeoutWaits(f *file, body *ast.BlockStmt) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch name, _ := f.pkgFunc(call, pkgWorkflow); name {
		case "AwaitWithTimeout":
			out = append(out, call)
		case "AwaitWithOptions":
			if len(call.Args) >= 2 {
				if lit := asLiteral(call.Args[1]); lit != nil {
					if _, ok := field(lit, "Timeout"); ok {
						out = append(out, call)
					}
				}
			}
		}
		return true
	})
	return out
}

// heartbeatSettings finds short HeartbeatTimeouts and Heartbeats as the
// first statement of a function.
func heartbeatSettings(r *repo, rule Rule) []hit {
	threshold := time.Duration(rule.Parameters["heartbeat_timeout_threshold_seconds"] * float64(time.Second))
	if threshold <= 0 {
		threshold = 10 * time.Second
	}
	var out []hit
	for _, f := range r.files {
		if f.has(pkgWorkflow) {
			ast.Inspect(f.ast, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !f.isPkgType(lit.Type, pkgWorkflow, "ActivityOptions") {
					return true
				}
				v, ok := field(lit, "HeartbeatTimeout")
				if !ok {
					return true
				}
				if d, ok := constDuration(f, v); ok && d > 0 && d < threshold {
					out = append(out, hit{
						id: rule.ID, file: f.path, line: f.line(v),
						note: fmt.Sprintf("HeartbeatTimeout of %s: Heartbeats reach the server every %s, one Action each", d, time.Duration(float64(d)*0.8)),
					})
				}
				return true
			})
		}
		if f.has(pkgActivity) {
			ast.Inspect(f.ast, func(n ast.Node) bool {
				var body *ast.BlockStmt
				switch fn := n.(type) {
				case *ast.FuncDecl:
					body = fn.Body
				case *ast.FuncLit:
					body = fn.Body
				default:
					return true
				}
				if body == nil || len(body.List) == 0 {
					return true
				}
				es, ok := body.List[0].(*ast.ExprStmt)
				if !ok {
					return true
				}
				if call, ok := es.X.(*ast.CallExpr); ok {
					if name, ok := f.pkgFunc(call, pkgActivity); ok && name == "RecordHeartbeat" {
						out = append(out, hit{
							id: rule.ID, file: f.path, line: f.line(call),
							note: "Heartbeat as the first statement: the first Heartbeat of each attempt is sent at once, so every run pays one Action",
						})
					}
				}
				return true
			})
		}
	}
	return out
}

// constDuration evaluates constant duration expressions such as
// 5 * time.Second, time.Second * 5, 500 * time.Millisecond, or
// time.Duration(2) * time.Minute.
func constDuration(f *file, e ast.Expr) (time.Duration, bool) {
	units := map[string]time.Duration{
		"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond,
		"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour,
	}
	var eval func(ast.Expr) (float64, bool)
	eval = func(e ast.Expr) (float64, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind == token.INT || x.Kind == token.FLOAT {
				v, err := strconv.ParseFloat(x.Value, 64)
				return v, err == nil
			}
		case *ast.ParenExpr:
			return eval(x.X)
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && f.imports[id.Name] == pkgTime {
				if u, ok := units[x.Sel.Name]; ok {
					return float64(u), true
				}
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Duration" && len(x.Args) == 1 {
				if id, ok := sel.X.(*ast.Ident); ok && f.imports[id.Name] == pkgTime {
					return eval(x.Args[0])
				}
			}
		case *ast.BinaryExpr:
			a, ok1 := eval(x.X)
			b, ok2 := eval(x.Y)
			if !ok1 || !ok2 {
				return 0, false
			}
			switch x.Op {
			case token.MUL:
				return a * b, true
			case token.QUO:
				if b != 0 {
					return a / b, true
				}
			case token.ADD:
				return a + b, true
			case token.SUB:
				return a - b, true
			}
		}
		return 0, false
	}
	v, ok := eval(e)
	return time.Duration(v), ok
}

// encryptWithoutCompression finds codec chains that encrypt without
// compressing first.
func encryptWithoutCompression(r *repo, rule Rule) []hit {
	encNames, compNames := rule.names("encrypt_names"), rule.names("compress_names")
	classify := func(e ast.Expr) (enc, comp bool) {
		s := strings.ToLower(exprString(e))
		for _, n := range encNames {
			enc = enc || strings.Contains(s, n)
		}
		for _, n := range compNames {
			comp = comp || strings.Contains(s, n)
		}
		return enc, comp
	}
	var out []hit
	for _, f := range r.files {
		if !f.has(pkgConverter) {
			continue
		}
		walk(f.ast, func(n ast.Node, stack []ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			if name, ok := f.pkgFunc(call, pkgConverter); !ok || name != "NewCodecDataConverter" || len(call.Args) < 2 {
				return
			}
			codecs := call.Args[1:]
			if call.Ellipsis.IsValid() && len(codecs) == 1 {
				// codecs... : expand a local slice literal if there is one.
				id, ok := codecs[0].(*ast.Ident)
				if !ok {
					return
				}
				_, body := enclosingFunc(stack)
				if body == nil {
					return
				}
				lit := localLiteral(body, id.Name, call.Pos())
				if lit == nil {
					return
				}
				codecs = lit.Elts
			}
			firstEnc, lastComp := -1, -1
			for i, c := range codecs {
				enc, comp := classify(c)
				if enc && firstEnc < 0 {
					firstEnc = i
				}
				if comp {
					lastComp = i
				}
			}
			switch {
			case firstEnc < 0:
				return
			case lastComp < 0:
				out = append(out, hit{id: rule.ID, file: f.path, line: f.line(call),
					note: "the codec chain encrypts but never compresses; storage is billed on the encrypted bytes"})
			case lastComp < firstEnc:
				// Codecs encode last to first, so a compression codec listed
				// before the encryption codec runs after it.
				out = append(out, hit{id: rule.ID, file: f.path, line: f.line(call),
					note: "the compression codec runs after encryption (codecs encode last to first), so it can't shrink the payload"})
			}
		})
	}
	return out
}

// scheduleCronUpdate finds Schedule updates that set CronExpressions on the
// existing spec without replacing the spec.
func scheduleCronUpdate(r *repo, rule Rule) []hit {
	var out []hit
	for _, f := range r.files {
		if !f.has(pkgClient) {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !f.isPkgType(lit.Type, pkgClient, "ScheduleUpdateOptions") {
				return true
			}
			v, ok := field(lit, "DoUpdate")
			if !ok {
				return true
			}
			fn, ok := v.(*ast.FuncLit)
			if !ok {
				return true
			}
			var cron *ast.AssignStmt
			replaces := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					s := exprString(lhs)
					switch {
					case strings.HasSuffix(s, ".CronExpressions"):
						if cron == nil {
							cron = as
						}
					case strings.HasSuffix(s, ".Spec"), strings.HasSuffix(s, ".Calendars"):
						replaces = true
					}
				}
				return true
			})
			if cron != nil && !replaces {
				out = append(out, hit{id: rule.ID, file: f.path, line: f.line(cron),
					note: "sets CronExpressions on the existing spec; each update appends duplicate calendar entries"})
			}
			return true
		})
	}
	return out
}

// startThenMessage finds a Workflow start followed by a Signal or Update to
// the same Workflow ID in the same function.
func startThenMessage(r *repo, rule Rule) []hit {
	var out []hit
	for _, f := range r.files {
		if !f.has(pkgClient) {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			var body *ast.BlockStmt
			switch fn := n.(type) {
			case *ast.FuncDecl:
				body = fn.Body
			case *ast.FuncLit:
				body = fn.Body
			default:
				return true
			}
			if body == nil {
				return true
			}
			out = append(out, startsInBody(f, body, rule)...)
			return true
		})
	}
	return dedupeHits(out)
}

type start struct {
	pos    token.Pos
	line   int
	id     string // expression text of StartWorkflowOptions.ID
	runVar string // variable holding the WorkflowRun
}

func startsInBody(f *file, body *ast.BlockStmt, rule Rule) []hit {
	var starts []start
	var out []hit
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false // inner functions are checked on their own
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Rhs) == 1 {
				if call, ok := s.Rhs[0].(*ast.CallExpr); ok {
					if st, ok := startCall(f, body, call); ok {
						if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
							st.runVar = id.Name
						}
						starts = append(starts, st)
						return false
					}
				}
			}
		case *ast.CallExpr:
			if st, ok := startCall(f, body, s); ok {
				starts = append(starts, st)
				return true
			}
			m, _ := f.method(s)
			var target, msg, name string
			switch {
			case m == "SignalWorkflow" && len(s.Args) >= 4:
				target, msg, name = exprString(s.Args[1]), "Signal", literalString(s.Args[3])
			case m == "UpdateWorkflow" && len(s.Args) >= 2:
				if lit := asLiteral(s.Args[1]); lit != nil {
					if v, ok := field(lit, "WorkflowID"); ok {
						target, msg = exprString(v), "Update"
					}
					if v, ok := field(lit, "UpdateName"); ok {
						name = literalString(v)
					}
				}
			}
			if target == "" {
				return true
			}
			for _, st := range starts {
				if st.pos >= s.Pos() {
					continue
				}
				if (st.id != "" && st.id == target) || (st.runVar != "" && target == st.runVar+".GetID()") {
					fix := "SignalWithStartWorkflow"
					if msg == "Update" {
						fix = "UpdateWithStartWorkflow"
					}
					h := hit{id: rule.ID, file: f.path, line: f.line(s),
						note: fmt.Sprintf("%s to the Workflow started at line %d; %s does both in one request", msg, st.line, fix)}
					if name != "" {
						h.keys = []string{name}
					}
					out = append(out, h)
					break
				}
			}
		}
		return true
	})
	return out
}

// startCall recognizes c.ExecuteWorkflow(ctx, opts, ...) and returns the
// Workflow ID expression when it can see it.
func startCall(f *file, body *ast.BlockStmt, call *ast.CallExpr) (start, bool) {
	m, _ := f.method(call)
	if m != "ExecuteWorkflow" || len(call.Args) < 3 {
		return start{}, false
	}
	st := start{pos: call.Pos(), line: f.line(call)}
	opts := asLiteral(call.Args[1])
	if opts == nil {
		if id, ok := call.Args[1].(*ast.Ident); ok {
			opts = localLiteral(body, id.Name, call.Pos())
		}
	}
	if opts != nil {
		if v, ok := field(opts, "ID"); ok {
			st.id = exprString(v)
		}
	}
	return st, true
}

func dedupeHits(hs []hit) []hit {
	seen := map[string]bool{}
	var out []hit
	for _, h := range hs {
		k := fmt.Sprintf("%s:%d:%s", h.file, h.line, h.id)
		if !seen[k] {
			seen[k] = true
			out = append(out, h)
		}
	}
	return out
}

// externalNameHints mark Activity names that point at an external call.
var externalNameHints = []string{"http", "api", "webhook", "fetch", "request", "call", "client"}

// externalCallsWithoutRetryLimit finds Activities that make HTTP calls,
// executed with options that set no MaximumAttempts.
func externalCallsWithoutRetryLimit(r *repo, rule Rule) []hit {
	var out []hit
	for _, f := range r.files {
		if !f.has(pkgWorkflow) {
			continue
		}
		walk(f.ast, func(n ast.Node, stack []ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			if name, ok := f.pkgFunc(call, pkgWorkflow); !ok || name != "ExecuteActivity" || len(call.Args) < 2 {
				return
			}
			_, body := enclosingFunc(stack)
			if body == nil {
				return
			}
			opts := activityOptions(f, body, call.Args[0], call.Pos())
			if opts == nil {
				return // can't see the options
			}
			if limited(f, body, opts, call.Pos()) {
				return
			}
			actName, why := externalActivity(r, call.Args[1])
			if why == "" {
				return
			}
			out = append(out, hit{id: rule.ID, file: f.path, line: f.line(call), keys: []string{actName},
				note: fmt.Sprintf("Activity %s %s, and its options set no MaximumAttempts, so it retries until a timeout; every retry is an Action", actName, why)})
		})
	}
	return out
}

// activityOptions finds the ActivityOptions literal behind the context
// passed to ExecuteActivity.
func activityOptions(f *file, body *ast.BlockStmt, ctxArg ast.Expr, pos token.Pos) *ast.CompositeLit {
	fromWith := func(call *ast.CallExpr) *ast.CompositeLit {
		if name, ok := f.pkgFunc(call, pkgWorkflow); !ok || name != "WithActivityOptions" || len(call.Args) < 2 {
			return nil
		}
		if lit := asLiteral(call.Args[1]); lit != nil {
			return lit
		}
		if id, ok := call.Args[1].(*ast.Ident); ok {
			return localLiteral(body, id.Name, call.Pos())
		}
		return nil
	}
	switch c := ctxArg.(type) {
	case *ast.CallExpr:
		return fromWith(c)
	case *ast.Ident:
		var found *ast.CompositeLit
		ast.Inspect(body, func(n ast.Node) bool {
			if n == nil || n.Pos() >= pos {
				return false
			}
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != c.Name || i >= len(as.Rhs) {
					continue
				}
				if call, ok := as.Rhs[i].(*ast.CallExpr); ok {
					if lit := fromWith(call); lit != nil {
						found = lit
					}
				}
			}
			return true
		})
		return found
	}
	return nil
}

// limited reports whether the options set RetryPolicy.MaximumAttempts to
// something other than 0. A RetryPolicy tcopt can't see counts as limited,
// so the rule stays quiet rather than guess.
func limited(f *file, body *ast.BlockStmt, opts *ast.CompositeLit, pos token.Pos) bool {
	rp, ok := field(opts, "RetryPolicy")
	if !ok {
		return false
	}
	lit := asLiteral(rp)
	if lit == nil {
		if id, ok := rp.(*ast.Ident); ok {
			lit = localLiteral(body, id.Name, pos)
		}
	}
	if lit == nil {
		return true
	}
	v, ok := field(lit, "MaximumAttempts")
	if !ok {
		return false
	}
	if bl, ok := v.(*ast.BasicLit); ok && bl.Value == "0" {
		return false
	}
	return true
}

// externalActivity says why an Activity looks like an external call, or ""
// when it doesn't.
func externalActivity(r *repo, e ast.Expr) (name, why string) {
	switch x := e.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	case *ast.BasicLit:
		name = literalString(x)
	default:
		return "", ""
	}
	for _, ref := range r.funcs[name] {
		if callsHTTP(ref.f, ref.decl.Body) {
			return name, "makes HTTP calls"
		}
	}
	lower := strings.ToLower(name)
	for _, h := range externalNameHints {
		if strings.Contains(lower, h) {
			return name, fmt.Sprintf("looks like an external call (%q in its name)", h)
		}
	}
	return name, ""
}

func callsHTTP(f *file, body *ast.BlockStmt) bool {
	if body == nil || !f.has(pkgHTTP) {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		if name, ok := f.pkgFunc(call, pkgHTTP); ok {
			switch name {
			case "Get", "Post", "Head", "PostForm", "NewRequest", "NewRequestWithContext":
				found = true
			}
		}
		if m, _ := f.method(call); m == "Do" {
			found = true
		}
		return !found
	})
	return found
}

// queryReturnsVisibleState finds Query handlers that only return a value
// the Workflow also writes to a Search Attribute or memo.
func queryReturnsVisibleState(r *repo, rule Rule) []hit {
	var out []hit
	for _, f := range r.files {
		if !f.has(pkgWorkflow) {
			continue
		}
		walk(f.ast, func(n ast.Node, stack []ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			name, ok := f.pkgFunc(call, pkgWorkflow)
			if !ok || (name != "SetQueryHandler" && name != "SetQueryHandlerWithOptions") || len(call.Args) < 3 {
				return
			}
			fn, ok := call.Args[2].(*ast.FuncLit)
			if !ok || len(fn.Body.List) != 1 {
				return
			}
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return
			}
			root := rootIdent(ret.Results[0])
			if root == "" {
				return
			}
			_, body := enclosingFunc(stack)
			if body == nil {
				return
			}
			if !upsertsIdent(f, body, root) {
				return
			}
			qt := literalString(call.Args[1])
			out = append(out, hit{id: rule.ID, file: f.path, line: f.line(call), keys: []string{qt},
				note: fmt.Sprintf("Query %q returns %s, which the Workflow also writes to a Search Attribute or memo; Describe reads it without an Action", qt, root)})
		})
	}
	return out
}

// rootIdent returns the variable at the root of x or x.Field.
func rootIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			if x.Name == "nil" || x.Name == "true" || x.Name == "false" {
				return ""
			}
			return x.Name
		case *ast.SelectorExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return ""
		}
	}
}

func upsertsIdent(f *file, body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		fn, ok := f.pkgFunc(call, pkgWorkflow)
		if !ok || (fn != "UpsertTypedSearchAttributes" && fn != "UpsertSearchAttributes" && fn != "UpsertMemo") {
			return true
		}
		for _, a := range call.Args[1:] {
			ast.Inspect(a, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == name {
					found = true
				}
				return !found
			})
		}
		return !found
	})
	return found
}
