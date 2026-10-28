package storage

import (
	"fmt"
	"slices"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds. Each one is documented in the detector's guidance file.
const (
	// LargePayload is the payload size STO-001 flags.
	LargePayload = 256 << 10
	// ReferenceBytes is what a claim-check reference costs instead of the
	// payload it replaces.
	ReferenceBytes = 256
)

func init() {
	detect.Register(sto001{})
}

const bytesPerGB = 1e9

// end is when the run closed, or c.Now while it's open.
func end(ex *model.Execution, c *detect.Context) time.Time {
	if !ex.CloseTime.IsZero() {
		return ex.CloseTime
	}
	return c.Now
}

// closed reports whether the run has closed, so its history sits in
// Retained Storage for the retention period.
func closed(ex *model.Execution) bool { return !ex.CloseTime.IsZero() }

// gbHours prices bytes that sit in history from `from` until the run ends:
//
//	active   = bytes / 1e9 × hours from `from` to close (or to Now while open)
//	retained = bytes / 1e9 × retention hours, once the run has closed
//
// Retained is 0 while the run is open or the retention is unknown.
func gbHours(bytes float64, from time.Time, ex *model.Execution, c *detect.Context) (active, retained float64) {
	if bytes <= 0 {
		return 0, 0
	}
	if h := end(ex, c).Sub(from).Hours(); h > 0 {
		active = bytes / bytesPerGB * h
	}
	if closed(ex) && c.RetentionHours > 0 {
		retained = bytes / bytesPerGB * c.RetentionHours
	}
	return active, retained
}

func mib(b float64) string { return fmt.Sprintf("%.1f MiB", b/(1<<20)) }

// sto001 finds inputs, results, Signals, and Updates over LargePayload. A
// claim check stores the data elsewhere and passes a ReferenceBytes
// reference, so each large payload saves its size less the reference.
type sto001 struct{}

func (sto001) ID() string                    { return "STO-001" }
func (sto001) Confidence() detect.Confidence { return detect.High }

func (sto001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var ids []int64
	var total, active, retained float64
	add := func(id int64, size int, at time.Time) {
		if size <= LargePayload {
			return
		}
		saved := float64(size - ReferenceBytes)
		a, r := gbHours(saved, at, ex, c)
		ids = append(ids, id)
		total += saved
		active += a
		retained += r
	}
	add(ex.Start.EventID, ex.Start.InputBytes, ex.StartTime)
	for _, a := range ex.Activities {
		add(a.ScheduledEventID, a.InputBytes, a.ScheduledTime)
		if a.ClosedEventID != 0 {
			add(a.ClosedEventID, a.ResultBytes, a.ClosedTime)
		}
	}
	for _, s := range ex.Signals {
		add(s.EventID, s.InputBytes, s.Time)
	}
	for _, u := range ex.Updates {
		add(u.AcceptedEventID, u.InputBytes, u.Time)
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return []detect.Match{{
		EventIDs:             ids,
		Note:                 fmt.Sprintf("%d payloads over %d KiB, %s in all", len(ids), LargePayload>>10, mib(total)),
		ActiveGBHoursSaved:   active,
		RetainedGBHoursSaved: retained,
	}}
}
