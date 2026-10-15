Before: a Timer, one Action, on every Update.

```go
_ = workflow.SetUpdateHandler(ctx, "approve", func(ctx workflow.Context) error {
	ok, err := workflow.AwaitWithTimeout(ctx, time.Minute, func() bool { return ready })
	if err == nil && !ok {
		return errors.New("not ready")
	}
	return err
})
```

After: no Timer.

```go
_ = workflow.SetUpdateHandler(ctx, "approve", func(ctx workflow.Context) error {
	return workflow.Await(ctx, func() bool { return ready })
})
```
