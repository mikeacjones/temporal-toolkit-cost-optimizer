# Temporal cost optimizer

`tcopt` reads a Namespace's Workflow histories, finds patterns that waste Temporal Cloud Actions, and estimates how many Actions fixing each one would save a month. Every finding cites the Event IDs that prove it. No LLM is involved.

It has:

- history detectors for the patterns in [docs/PLAN.md](docs/PLAN.md) section 6.4: Search Attribute upserts, Activity retry limits and unregistered Activities, Local Activities that outlast the Workflow Task timeout, thin Workflows and Child Workflows, leading Timers, start-then-Signal, and large payloads
- each Workflow Type's current Actions per run and per month, from billing rules kept as data in `kb/billing/actions.yaml`
- reviewed fix guidance for every detector in `kb/guidance/`, and a feature catalog that says whether your SDK version has a newer feature a fix needs
- `tcopt`, which opens the TUI, and `tcopt analyze`, a headless run that writes Markdown or JSON

Every saving is in Actions, or GB-hours for storage. Nothing is priced in currency.

`tcopt` only reads the Namespace you point it at. Reading histories, listing, and counting executions aren't billable Actions, but they count against the Namespace's requests per second, so `tcopt` limits itself to 10 per second by default (`--rps`).

## Run it

Run `tcopt` to open the TUI. The first time, it asks how to reach the Namespace you want to analyze:

- Log in to Temporal Cloud in your browser and pick a Namespace. `tcopt` then creates an API key that expires in 7 days, after you confirm it, and keeps the key in your OS keychain.
- Pick a profile from the Temporal CLI's `temporal.toml`.
- Use `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, and `TEMPORAL_API_KEY`, if they're set.

`tcopt` remembers the choice. Press `n` in the overview to pick another Namespace. `--profile`, `TEMPORAL_PROFILE`, or `TEMPORAL_ADDRESS` skip the choice for one run, and `tcopt` never falls back to the `default` profile on its own.

```sh
go build -o bin/tcopt ./cmd/tcopt
bin/tcopt                                # opens the TUI
bin/tcopt analyze --window 168h -o report.md
bin/tcopt kb SA-002
```

Without `--types`, `tcopt` analyzes the five Workflow Types with the most executions in the window, 200 sampled executions each.

- To say whether your SDK has a feature a fix needs, `tcopt` reads lockfiles in a repo: `--repo ~/src/orders` (repeatable), or `repos` in `~/.config/tcopt/config.toml`.
- Findings whose only fix is a pre-release feature are hidden unless you pass `--include-preview`.

## Try it locally

You need Go, [`just`](https://github.com/casey/just), and the `temporal` CLI.

```sh
just dev-server      # in one terminal
just demo-load 300   # starts 300 executions of the workloads in internal/workloads
just demo-analyze    # report on stdout
just demo-tui
```

| Command | What it does |
|---|---|
| `just test` | Unit tests, including the golden histories in `testdata/histories` |
| `just goldens` | Re-records the golden histories from the workloads |
| `just build-brand` | Builds with Ziggy |
| `just ziggy` | Regenerates the Ziggy sprite from the buildkit's brand assets |

## Notes

- Ziggy only appears in builds with the `brand` tag, until brand approves using the mascot in a tool customers install (PLAN.md, section 14).
- The TUI runs the analysis in-process for now. Phase 2 moves it into the Session and Analysis Workflows.
- Estimates come from Event History. Queries, Activity Heartbeats, rejected Updates, Schedules, Export, and replication aren't in history, so reports leave them out and say so.
