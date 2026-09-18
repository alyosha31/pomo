package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type timerState struct {
	End       int64 `json:"end"`
	Paused    bool  `json:"paused"`
	Remaining int64 `json:"remaining,omitempty"`
	Notified  bool  `json:"notified,omitempty"`
}

// claimCompletion returns true exactly once for a completed timer, even when
// several tmux clients refresh the status line at the same time.
func claimCompletion(now time.Time) (bool, error) {
	path, err := statePath()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	s, err := loadState()
	if err != nil {
		return false, err
	}
	if s.remaining(now) > 0 || s.Notified {
		return false, nil
	}
	s.Notified = true
	if err := saveState(s); err != nil {
		return false, err
	}
	return true, nil
}

func newRunningState(d time.Duration, now time.Time) timerState {
	return timerState{End: now.Add(d).UnixNano()}
}

func (s timerState) remaining(now time.Time) time.Duration {
	if s.Paused {
		return time.Duration(s.Remaining)
	}
	return time.Unix(0, s.End).Sub(now)
}

func statePath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "pomo", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "pomo", "state.json"), nil
}

func loadState() (timerState, error) {
	path, err := statePath()
	if err != nil {
		return timerState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return timerState{}, err
	}
	var s timerState
	if err := json.Unmarshal(data, &s); err != nil {
		return timerState{}, err
	}
	return s, nil
}

func saveState(s timerState) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func deleteState() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
