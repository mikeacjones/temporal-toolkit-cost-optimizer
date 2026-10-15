package config

import (
	"errors"
	"testing"
)

func TestNoProfileNoAddressRefuses(t *testing.T) {
	t.Setenv("TEMPORAL_PROFILE", "")
	t.Setenv("TEMPORAL_ADDRESS", "")
	if _, err := DialProfile("", "", ""); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("got %v, want ErrNoProfile", err)
	}
}
