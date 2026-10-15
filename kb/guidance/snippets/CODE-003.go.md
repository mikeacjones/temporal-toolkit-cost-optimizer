Before: a Heartbeat every 1.6 seconds, and one Action at the start of every run.

```go
ao := workflow.ActivityOptions{StartToCloseTimeout: time.Hour, HeartbeatTimeout: 2 * time.Second}

func Sync(ctx context.Context, items []string) error {
	activity.RecordHeartbeat(ctx, "started")
	for i, it := range items {
		process(it)
		activity.RecordHeartbeat(ctx, i)
	}
	return nil
}
```

After: Heartbeats only as work progresses, at most one every 48 seconds.

```go
ao := workflow.ActivityOptions{StartToCloseTimeout: time.Hour, HeartbeatTimeout: time.Minute}

func Sync(ctx context.Context, items []string) error {
	for i, it := range items {
		process(it)
		activity.RecordHeartbeat(ctx, i)
	}
	return nil
}
```
