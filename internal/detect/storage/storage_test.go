package storage

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

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

func TestOpenRunsHaveNoRetainedSaving(t *testing.T) {
	ex := detecttest.Golden(t, "sto001_positive")
	ex.CloseTime = time.Time{}
	if _, r := gbHours(1e9, ex.StartTime, ex, detecttest.Context(t, time.Time{})); r != 0 {
		t.Errorf("retained %v for an open run, want 0", r)
	}
}
