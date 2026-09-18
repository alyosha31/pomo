package main

import (
	"testing"
	"time"
)

func TestDefaultFocusDuration(t *testing.T) {
	got, err := durationArg(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 40*time.Minute {
		t.Fatalf("default duration = %v, want 40m", got)
	}
}
