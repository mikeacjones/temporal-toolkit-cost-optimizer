Before: each update adds the new cron expression's calendar entries to the old ones.

```go
DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
	in.Description.Schedule.Spec.CronExpressions = []string{cron}
	return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
},
```

After: a fresh spec replaces the old one.

```go
DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
	in.Description.Schedule.Spec = &client.ScheduleSpec{CronExpressions: []string{cron}}
	return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
},
```
