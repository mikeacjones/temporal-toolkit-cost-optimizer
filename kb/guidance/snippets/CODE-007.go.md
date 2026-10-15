Before: a start and a Signal.

```go
run, err := c.ExecuteWorkflow(ctx, opts, OrderWorkflow, order)
if err != nil {
	return err
}
return c.SignalWorkflow(ctx, run.GetID(), "", "paid", payment)
```

After: one Signal-With-Start.

```go
_, err := c.SignalWithStartWorkflow(ctx, opts.ID, "paid", payment, opts, OrderWorkflow, order)
return err
```

For an Update, use Update-With-Start:

```go
op := c.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
	ID: cartID, TaskQueue: "carts",
	WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
}, CartWorkflow)
h, err := c.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
	StartWorkflowOperation: op,
	UpdateOptions: client.UpdateWorkflowOptions{UpdateName: "addItem", Args: []any{item},
		WaitForStage: client.WorkflowUpdateStageCompleted},
})
```
