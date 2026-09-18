package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClockRoundsUp(t *testing.T) {
	cases := map[time.Duration]string{
		0: "00:00", time.Millisecond: "00:01", 59 * time.Second: "00:59",
		60 * time.Second: "01:00", 25 * time.Minute: "25:00",
	}
	for input, want := range cases {
		if got := clock(input); got != want {
			t.Errorf("clock(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	want := newRunningState(25*time.Minute, time.Now())
	if err := saveState(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, "pomo", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state permissions = %o", info.Mode().Perm())
	}
}

func TestPausedRemaining(t *testing.T) {
	s := timerState{Paused: true, Remaining: int64(90 * time.Second)}
	if got := s.remaining(time.Now()); got != 90*time.Second {
		t.Fatalf("got %v", got)
	}
}
