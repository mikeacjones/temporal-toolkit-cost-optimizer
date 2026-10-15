Before: one Action per Query, every five seconds, for every order.

```go
for range ticker.C {
	v, _ := c.QueryWorkflow(ctx, id, "", "status")
	_ = v.Get(&status)
}
```

After: the Workflow upserts the status when it changes, and callers read it with Describe.

```go
// In the Workflow, once per change:
_ = workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet(status))

// In the caller:
desc, err := c.DescribeWorkflowExecution(ctx, id, "")
if err != nil {
	return err
}
p := desc.GetWorkflowExecutionInfo().GetSearchAttributes().GetIndexedFields()["OrderStatus"]
```
