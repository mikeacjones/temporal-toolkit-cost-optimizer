---
id: CODE-010
title: Status read with a Query where Describe works
why_it_costs: Each Query is one Action. The Query handler returns a value the Workflow already writes to a Search Attribute or memo, which Describe and List read without an Action.
fix: Read the value from the Search Attribute or memo with Describe or List, and remove the Query handler if nothing else needs it.
tradeoffs: A Search Attribute or memo shows the value as of the last upsert, while a Query runs the handler on a Worker. Describe and List share a Visibility rate limit.
docs:
  - kb: cost/evaluate-cloud-actions#query
snippets:
  go: snippets/CODE-010.go.md
locator:
  sdk_calls:
    go: ["workflow.SetQueryHandler", "workflow.UpsertTypedSearchAttributes", "workflow.UpsertMemo"]
---

The Workflow answers a Query with a variable it also upserts as a Search Attribute or memo. Callers that use the Query pay one Action per read for data they could get for free.

Read it with Describe for one execution, or List with a query on the Search Attribute for many.
