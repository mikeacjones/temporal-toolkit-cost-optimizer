---
id: CODE-002
title: Wait with a timeout inside an Update or Signal handler
why_it_costs: The SDK starts a Timer for every wait with a timeout, and each Timer is one Action, even when the condition is met before the Timer fires.
fix: Wait without a timeout where the handler doesn't need one, and let the Workflow's own timeout bound the wait.
tradeoffs: Without a timeout, the handler can wait for as long as the Workflow runs. Keep the timeout where callers need a bounded answer, and drain waiting handlers before Continue-As-New.
docs:
  - kb: cost/evaluate-cloud-actions#timer
snippets:
  go: snippets/CODE-002.go.md
locator:
  sdk_calls:
    go: ["workflow.AwaitWithTimeout", "workflow.AwaitWithOptions", "workflow.SetUpdateHandler"]
---

An Update or Signal handler waits for a condition with a timeout. The SDK implements the timeout with a Timer, so every call to the handler starts one and pays one Action. In history this shows as TimerStarted followed by TimerCanceled (TMR-003).

If the handler can wait until the condition is true, use `workflow.Await` with no timeout. If callers need an answer within a deadline, consider whether the caller's own request timeout already covers it.
