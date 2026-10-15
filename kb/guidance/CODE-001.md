---
id: CODE-001
title: Query polling in client code
why_it_costs: Each Query is one Action. A poller or dashboard that queries on a schedule pays for every Query, whether or not anything changed.
fix: Read status with Describe or List, which aren't Actions, from a Search Attribute or memo the Workflow sets when the status changes.
tradeoffs: The Workflow spends one Action per status change on the upsert, so this saves Actions when status changes less often than callers would query. Describe and List share a Visibility rate limit.
docs:
  - kb: cost/evaluate-cloud-actions#query
  - kb: cost/best-practices-cost-optimization
snippets:
  go: snippets/CODE-001.go.md
locator:
  sdk_calls:
    go: ["client.Client.QueryWorkflow", "client.Client.QueryWorkflowWithOptions"]
---

Code outside a Workflow queries it from a loop, a ticker, or an HTTP handler. Each Query goes to a Worker and is billed as one Action, so the cost grows with how often callers ask, not with how often the answer changes.

Have the Workflow write the status where callers can read it without a Query: upsert a Search Attribute or memo when the status changes. Callers then read it with Describe for one execution, or List for many. Neither is an Action.
