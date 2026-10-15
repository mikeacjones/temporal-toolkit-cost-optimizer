// Command genhistories runs the workload groups in internal/workloads
// against a Temporal server and records each run's history as a golden file
// in testdata/histories. With -load it instead starts many executions to
// fill a Namespace for end-to-end runs.
//
// Start a dev server that registers the groups' Search Attributes first;
// `just dev-server` does. Each group runs on its own Task Queue, so several
// groups can record against one server at once.
//
//	go run ./tools/genhistories -address localhost:17233 -group timers
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads/all"
)

func main() {
	addr := flag.String("address", "localhost:7233", "Temporal frontend address")
	ns := flag.String("namespace", "default", "Namespace to run in")
	out := flag.String("out", "testdata/histories", "directory for golden histories")
	only := flag.String("group", "", "comma-separated groups to run (default: all)")
	golden := flag.String("golden", "", "record only this golden")
	load := flag.Int("load", 0, "start this many executions of the groups' load mix instead of recording goldens")
	listSA := flag.Bool("search-attributes", false, "print the --search-attribute flags the groups need, then exit")
	flag.Parse()

	groups := selectGroups(*only)
	if *listSA {
		seen := map[string]bool{}
		for _, g := range workloads.Groups() {
			for _, sa := range g.SearchAttributes {
				if !seen[sa] {
					seen[sa] = true
					fmt.Printf("--search-attribute %s ", sa)
				}
			}
		}
		fmt.Println()
		return
	}

	c, err := client.Dial(client.Options{HostPort: *addr, Namespace: *ns})
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer c.Close()

	for _, g := range groups {
		stop, err := startWorkers(c, g)
		if err != nil {
			log.Fatalf("%s: workers: %v", g.Name, err)
		}
		defer stop()
	}

	ctx := context.Background()
	if *load > 0 {
		runLoad(ctx, c, groups, *load)
		return
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	failed := false
	for _, g := range groups {
		for _, r := range g.Runs {
			if *golden != "" && r.Golden != *golden {
				continue
			}
			if err := record(ctx, c, g, r, *out); err != nil {
				log.Printf("%s/%s: %v", g.Name, r.Golden, err)
				failed = true
			}
		}
	}
	if failed {
		os.Exit(1)
	}
}

func selectGroups(only string) []workloads.Group {
	all := workloads.Groups()
	if only == "" {
		return all
	}
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		want[strings.TrimSpace(n)] = true
	}
	var out []workloads.Group
	for _, g := range all {
		if want[g.Name] {
			out = append(out, g)
			delete(want, g.Name)
		}
	}
	for n := range want {
		log.Fatalf("no workload group %q", n)
	}
	return out
}

func startWorkers(c client.Client, g workloads.Group) (func(), error) {
	if g.Workers != nil {
		return g.Workers(c, g.TaskQueue())
	}
	w := worker.New(c, g.TaskQueue(), worker.Options{})
	g.Register(w)
	if err := w.Start(); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

func record(ctx context.Context, c client.Client, g workloads.Group, r workloads.Run, dir string) error {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := r.Options
	opts.ID = fmt.Sprintf("golden-%s-%d", r.Golden, time.Now().UnixNano())
	opts.TaskQueue = g.TaskQueue()
	wr, err := c.ExecuteWorkflow(ctx, opts, r.Workflow, r.Args...)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if r.Drive != nil {
		if err := r.Drive(ctx, c, wr); err != nil {
			return fmt.Errorf("drive: %w", err)
		}
	}
	if r.LeaveOpen {
		time.Sleep(r.Settle)
	} else if err := wr.Get(ctx, nil); err != nil && !r.ExpectFailure {
		return fmt.Errorf("run: %w", err)
	}

	h, err := fetch(ctx, c, wr.GetID(), wr.GetRunID())
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if r.LeaveOpen {
		_ = c.TerminateWorkflow(context.Background(), wr.GetID(), wr.GetRunID(), "golden recorded")
	}
	if err := write(filepath.Join(dir, r.Golden+".json"), h); err != nil {
		return err
	}
	if !r.Children {
		return nil
	}
	for _, ev := range h.GetEvents() {
		if ev.GetEventType() != enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_STARTED {
			continue
		}
		a := ev.GetChildWorkflowExecutionStartedEventAttributes()
		ch, err := fetch(ctx, c, a.GetWorkflowExecution().GetWorkflowId(), a.GetWorkflowExecution().GetRunId())
		if err != nil {
			return fmt.Errorf("child history: %w", err)
		}
		name := fmt.Sprintf("%s.child-%d.json", r.Golden, a.GetInitiatedEventId())
		if err := write(filepath.Join(dir, name), ch); err != nil {
			return err
		}
	}
	return nil
}

func write(path string, h *historypb.History) error {
	b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func runLoad(ctx context.Context, c client.Client, groups []workloads.Group, n int) {
	type pick struct {
		tq   string
		item workloads.LoadItem
	}
	var items []pick
	total := 0
	for _, g := range groups {
		for _, it := range g.Load {
			items = append(items, pick{g.TaskQueue(), it})
			total += it.Weight
		}
	}
	if total == 0 {
		log.Fatal("no load items in the selected groups")
	}
	var runs []client.WorkflowRun
	for i := range n {
		x := rand.IntN(total)
		var p pick
		for _, it := range items {
			if x < it.item.Weight {
				p = it
				break
			}
			x -= it.item.Weight
		}
		opts := p.item.Options
		opts.ID = fmt.Sprintf("load-%d-%d", i, rand.Int64())
		opts.TaskQueue = p.tq
		wr, err := c.ExecuteWorkflow(ctx, opts, p.item.Workflow, p.item.Args...)
		if err != nil {
			log.Fatalf("start: %v", err)
		}
		runs = append(runs, wr)
	}
	failed := 0
	for _, wr := range runs {
		if err := wr.Get(ctx, nil); err != nil {
			var appErr *temporal.ApplicationError
			if !errors.As(err, &appErr) {
				log.Printf("%s: %v", wr.GetID(), err)
			}
			failed++
		}
	}
	fmt.Printf("ran %d executions (%d ended in failure)\n", n, failed)
}

func fetch(ctx context.Context, c client.Client, id, runID string) (*historypb.History, error) {
	h := &historypb.History{}
	iter := c.GetWorkflowHistory(ctx, id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			return nil, err
		}
		h.Events = append(h.Events, ev)
	}
	return h, nil
}
