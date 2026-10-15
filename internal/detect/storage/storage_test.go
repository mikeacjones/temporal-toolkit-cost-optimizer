package storage

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// The goldens were recorded in seconds, so tests that need a run to have been
// open for days move Now forward, and STO-003 moves the start back.

func last(ex *model.Execution) time.Time { return lastEventTime(ex) }

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9+1e-6*math.Abs(b) }

func one(t *testing.T, ms []detect.Match) detect.Match {
	t.Helper()
	if len(ms) != 1 {
		t.Fatalf("got %d matches %+v, want 1", len(ms), ms)
	}
	return ms[0]
}

func none(t *testing.T, ms []detect.Match) {
	t.Helper()
	if len(ms) != 0 {
		t.Fatalf("got %d matches %+v, want none", len(ms), ms)
	}
}

func TestSTO001(t *testing.T) {
	ex := detecttest.Golden(t, "sto001_positive")
	c := detecttest.Context(t, time.Time{})
	m := one(t, sto001{}.Detect(ex, c))
	if !slices.Equal(m.EventIDs, []int64{7}) {
		t.Errorf("event IDs %v, want [7]", m.EventIDs)
	}
	saved := float64(ex.Activities[0].ResultBytes - ReferenceBytes)
	if want := saved / bytesPerGB * c.RetentionHours; !near(m.RetainedGBHoursSaved, want) {
		t.Errorf("retained %v, want %v", m.RetainedGBHoursSaved, want)
	}
	if m.ActiveGBHoursSaved <= 0 {
		t.Error("expected active GB-hours for the time the result sat in an open run")
	}
	none(t, sto001{}.Detect(detecttest.Golden(t, "sto001_negative"), c))
}

func TestSTO003(t *testing.T) {
	ex := detecttest.Golden(t, "sto003_positive")
	now := last(ex).Add(10 * time.Minute)
	ex.StartTime = last(ex).Add(-72 * time.Hour)
	m := one(t, sto003{}.Detect(ex, detecttest.Context(t, now)))
	open := now.Sub(ex.StartTime)
	if want := float64(ex.HistoryBytes) / bytesPerGB * (open - ContinueAsNewEvery).Hours() / 2; !near(m.ActiveGBHoursSaved, want) {
		t.Errorf("active %v, want %v", m.ActiveGBHoursSaved, want)
	}

	// Not open long enough.
	none(t, sto003{}.Detect(detecttest.Golden(t, "sto003_positive"), detecttest.Context(t, now)))
	// Open long enough, but no longer growing.
	stale := detecttest.Golden(t, "sto003_positive")
	stale.StartTime = last(stale).Add(-72 * time.Hour)
	none(t, sto003{}.Detect(stale, detecttest.Context(t, last(stale).Add(48*time.Hour))))
	// A small history.
	small := detecttest.Golden(t, "sto003_negative")
	small.StartTime = last(small).Add(-72 * time.Hour)
	none(t, sto003{}.Detect(small, detecttest.Context(t, last(small).Add(10*time.Minute))))
}

func TestSTO004(t *testing.T) {
	ex := detecttest.Golden(t, "sto004_positive")
	c := detecttest.Context(t, last(ex).Add(20*time.Hour))
	m := one(t, sto004{}.Detect(ex, c))
	timer := ex.Timers[len(ex.Timers)-1]
	if !slices.Equal(m.EventIDs, []int64{timer.StartedEventID}) {
		t.Errorf("event IDs %v, want [%d]", m.EventIDs, timer.StartedEventID)
	}
	if want := float64(ex.HistoryBytes) / bytesPerGB * 24; !near(m.ActiveGBHoursSaved, want) {
		t.Errorf("active %v, want %v", m.ActiveGBHoursSaved, want)
	}
	neg := detecttest.Golden(t, "sto004_negative")
	none(t, sto004{}.Detect(neg, detecttest.Context(t, last(neg).Add(20*time.Hour))))
	// Work that finished after the Timer started is STO-005's.
	done := detecttest.Golden(t, "sto005_positive")
	none(t, sto004{}.Detect(done, detecttest.Context(t, last(done).Add(20*time.Hour))))
}

func TestSTO005(t *testing.T) {
	ex := detecttest.Golden(t, "sto005_positive")
	c := detecttest.Context(t, last(ex).Add(2*time.Hour))
	m := one(t, sto005{}.Detect(ex, c))
	if !slices.Equal(m.EventIDs, []int64{5, 12}) {
		t.Errorf("event IDs %v, want [5 12]", m.EventIDs)
	}
	timer, act := ex.Timers[0], ex.Activities[0]
	hours := timer.StartTime.Add(timer.Duration).Sub(act.ClosedTime).Hours()
	if want := float64(ex.HistoryBytes) / bytesPerGB * hours; !near(m.ActiveGBHoursSaved, want) {
		t.Errorf("active %v, want %v", m.ActiveGBHoursSaved, want)
	}
	// Not idle long enough yet.
	none(t, sto005{}.Detect(ex, detecttest.Context(t, last(ex).Add(time.Minute))))
	none(t, sto005{}.Detect(detecttest.Golden(t, "sto005_negative"), c))
}

func TestSTO006(t *testing.T) {
	c := detecttest.Context(t, time.Time{})
	m := one(t, sto006{}.Detect(detecttest.Golden(t, "sto006_positive"), c))
	if len(m.EventIDs) != 10 || m.RetainedGBHoursSaved <= 0 {
		t.Errorf("got %d markers and %v retained GB-hours, want 10 and more than 0", len(m.EventIDs), m.RetainedGBHoursSaved)
	}
	none(t, sto006{}.Detect(detecttest.Golden(t, "sto006_negative"), c))
}

func TestSTO007(t *testing.T) {
	c := detecttest.Context(t, time.Time{})
	ex := detecttest.Golden(t, "sto007_positive")
	m := one(t, sto007{}.Detect(ex, c))
	if !slices.Equal(m.EventIDs, []int64{5, 11, 17, 23}) {
		t.Errorf("event IDs %v, want [5 11 17 23]", m.EventIDs)
	}
	if want := float64(3*ex.Activities[1].HeaderBytes) / bytesPerGB * c.RetentionHours; !near(m.RetainedGBHoursSaved, want) {
		t.Errorf("retained %v, want %v", m.RetainedGBHoursSaved, want)
	}
	none(t, sto007{}.Detect(detecttest.Golden(t, "sto007_negative"), c))
}

func TestOpenRunsHaveNoRetainedSaving(t *testing.T) {
	ex := detecttest.Golden(t, "sto004_positive")
	if _, r := gbHours(1e9, ex.StartTime, ex, detecttest.Context(t, time.Time{})); r != 0 {
		t.Errorf("retained %v for an open run, want 0", r)
	}
}
