---
id: CODE-009
title: No retry limit on an Activity that calls an external service
why_it_costs: The default retry policy retries until the Activity's schedule-to-close timeout, and every retry is an Action. During an outage of the other service, each waiting Activity keeps retrying.
fix: Set RetryPolicy.MaximumAttempts, raise the initial interval for calls that fail slowly, and mark errors that won't succeed on retry as non-retryable.
tradeoffs: A limit means an Activity can fail during a long outage. Decide what the Workflow should do then, such as wait and try again later, or alert someone.
docs:
  - kb: cost/evaluate-cloud-actions#activity
  - kb: design-patterns/fixed-count-retries
  - kb: design-patterns/non-retryable-errors
  - kb: design-patterns/delayed-retry
snippets:
  go: snippets/CODE-009.go.md
locator:
  sdk_calls:
    go: ["workflow.ExecuteActivity", "workflow.ActivityOptions", "temporal.RetryPolicy"]
---

The Activity makes HTTP calls, and the options it runs with set no MaximumAttempts. When the other service is down, every retry is an Action, for as long as the outage lasts or until the timeout ends it.

Pick a limit that covers the outages you expect to ride out. For errors that carry their own wait time, such as a 429 with Retry-After, return an Application Failure with a next retry delay instead of retrying on the default schedule.
