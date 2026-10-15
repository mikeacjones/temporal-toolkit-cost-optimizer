---
id: CODE-007
title: Start, then Signal or Update, in the same function
why_it_costs: A start followed by a Signal pays for both. Signal-With-Start is one Action in total, and Update-With-Start sends the start and the Update in one request.
fix: Use SignalWithStartWorkflow, or UpdateWithStartWorkflow with a Workflow ID conflict policy of Use Existing.
tradeoffs: None for Signal-With-Start. Update-With-Start needs an SDK and server that support it; check the feature status on the finding.
docs:
  - kb: cost/evaluate-cloud-actions#signal
  - kb: design-patterns/signal-with-start
  - kb: design-patterns/early-return
snippets:
  go: snippets/CODE-007.go.md
locator:
  sdk_calls:
    go: ["client.Client.ExecuteWorkflow", "client.Client.SignalWorkflow", "client.Client.UpdateWorkflow"]
---

The code starts a Workflow and then, in the same function, sends a Signal or Update to it. Each request is billed separately, and the second one also races the first Workflow Task.

Signal-With-Start starts the Workflow if it isn't running and delivers the Signal either way, as one Action. Update-With-Start does the same for an Update and returns its result in the same call.
