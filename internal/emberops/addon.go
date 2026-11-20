package emberops

import (
	"context"
	"net/url"
)

// The emberops_hearth helpers for time, beyond upsert/bulk_upsert.

// TimeEntryResult is what upsert_time_entry and timer_stop answer.
type TimeEntryResult struct {
	Timesheet string `json:"timesheet"`
	Row       string `json:"row"`
	Modified  string `json:"modified"`
}

// UpsertTimeEntry creates or updates one Timesheet Detail row by values["hearth_id"],
// filing it in its employee's Timesheet for the day. values must name the
// employee ("hearth:<principal id>") even when updating.
func (c *Client) UpsertTimeEntry(ctx context.Context, values map[string]any) (TimeEntryResult, error) {
	var r TimeEntryResult
	err := c.Call(ctx, "emberops_hearth.api.upsert_time_entry", map[string]any{"values": values}, &r)
	return r, err
}

// TimerStart starts the calling person's timer. One at a time per person.
func (c *Client) TimerStart(ctx context.Context, task, workOrder, note string, tags []string) error {
	args := map[string]any{"note": note, "tags": tags}
	if task != "" {
		args["task"] = task
	}
	if workOrder != "" {
		args["maintenance_work_order"] = workOrder
	}
	return c.Call(ctx, "emberops_hearth.api.timer_start", args, nil)
}

// TimerStop stops the calling person's timer and books it as the time entry
// with this Hearth id.
func (c *Client) TimerStop(ctx context.Context, hearthID, note string) (TimeEntryResult, error) {
	var r TimeEntryResult
	args := map[string]any{"hearth_id": hearthID}
	if note != "" {
		args["note"] = note
	}
	err := c.Call(ctx, "emberops_hearth.api.timer_stop", args, &r)
	return r, err
}

// WeekLoadResult is one person's week against their capacity.
type WeekLoadResult struct {
	Employee        string `json:"employee"`
	WeekStart       string `json:"week_start"`
	Minutes         int    `json:"minutes"`
	CapacityMinutes int    `json:"capacity_minutes"`
}

// WeekLoad is the minutes an employee logged in the seven days from weekStart
// (YYYY-MM-DD; empty for this Monday), next to their capacity. Soft-deleted
// entries are not counted. employee is a Frappe name; empty means the caller.
func (c *Client) WeekLoad(ctx context.Context, employee, weekStart string) (WeekLoadResult, error) {
	q := url.Values{}
	if employee != "" {
		q.Set("employee", employee)
	}
	if weekStart != "" {
		q.Set("week_start", weekStart)
	}
	var r WeekLoadResult
	err := c.CallGet(ctx, "emberops_hearth.api.week_load", q, &r)
	return r, err
}
