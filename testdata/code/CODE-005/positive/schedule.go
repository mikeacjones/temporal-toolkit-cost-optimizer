package sched

import (
	"context"

	"go.temporal.io/sdk/client"
)

func setCron(ctx context.Context, h client.ScheduleHandle, cron string) error {
	return h.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			in.Description.Schedule.Spec.CronExpressions = []string{cron} // want CODE-005
			return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
		},
	})
}
