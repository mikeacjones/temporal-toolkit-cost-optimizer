---
id: CODE-003
title: Heartbeat settings that drive Heartbeat volume
why_it_costs: Each Heartbeat that reaches the server is one Action. SDKs send the first Heartbeat of each attempt at once and throttle later ones to 0.8 times the HeartbeatTimeout, so a short timeout sends more, and a Heartbeat at the start of the Activity costs an Action on every run.
fix: Set HeartbeatTimeout to the longest gap you can accept before a stuck Activity is retried, and heartbeat as work progresses rather than first thing.
tradeoffs: A longer HeartbeatTimeout means a stuck or lost Worker is noticed later, and an Activity that never heartbeats can't be cancelled until it finishes.
docs:
  - kb: cost/evaluate-cloud-actions#activity
snippets:
  go: snippets/CODE-003.go.md
locator:
  sdk_calls:
    go: ["workflow.ActivityOptions", "activity.RecordHeartbeat"]
---

The number of Heartbeats Temporal bills depends on the HeartbeatTimeout, not on how often the code calls RecordHeartbeat: the SDK throttles calls to one every 0.8 times the timeout. A two-second timeout can send a Heartbeat every 1.6 seconds for as long as the Activity runs.

The first Heartbeat of each attempt isn't throttled. An Activity that heartbeats as its first statement pays one Action on every run, including runs that finish in milliseconds.
