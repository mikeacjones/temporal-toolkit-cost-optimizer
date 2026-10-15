package sched

import (
	"context"

	"go.temporal.io/sdk/client"
)

// Replacing the whole spec drops the calendars the server made from the old
// cron expressions.
func setCron(ctx context.Context, h client.ScheduleHandle, cron string) error {
	return h.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			in.Description.Schedule.Spec = &client.ScheduleSpec{CronExpressions: []string{cron}}
			return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
		},
	})
}
