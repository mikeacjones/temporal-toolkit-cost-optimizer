Before: one Action per read.

```go
v, err := c.QueryWorkflow(ctx, id, "", "status")
```

After: Describe reads the Search Attribute the Workflow already sets.

```go
desc, err := c.DescribeWorkflowExecution(ctx, id, "")
if err != nil {
	return err
}
p := desc.GetWorkflowExecutionInfo().GetSearchAttributes().GetIndexedFields()["OrderStatus"]
```
