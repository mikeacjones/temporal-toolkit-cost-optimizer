Before: retries until the timeout, one Action each.

```go
ao := workflow.ActivityOptions{StartToCloseTimeout: time.Minute}
```

After: at most ten attempts, starting ten seconds apart.

```go
ao := workflow.ActivityOptions{
	StartToCloseTimeout: time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:        10 * time.Second,
		BackoffCoefficient:     2,
		MaximumAttempts:        10,
		NonRetryableErrorTypes: []string{"InvalidCard"},
	},
}
```
