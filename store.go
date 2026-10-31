package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Estimate sources for a task's pomodoro count.
const (
	SourceAI      = "ai"      // inferred by the planner
	SourceDefault = "default" // planner could not tell; placeholder of 1
	SourceManual  = "manual"  // set by the user
)

type Task struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	Pomodoros int        `json:"pomodoros"`
	Completed int        `json:"completed"`
	Source    string     `json:"source"`
	Done      bool       `json:"done"`
	CreatedAt time.Time  `json:"createdAt"`
	DoneAt    *time.Time `json:"doneAt,omitempty"`
}

// Timer is driven by the browser; the server only persists it so a refresh
// or a second device picks up where the session left off.
type Timer struct {
	Phase     string `json:"phase"` // idle | work | short | long
	TaskID    string `json:"taskId"`
	Duration  int64  `json:"duration"`  // ms
	EndsAt    int64  `json:"endsAt"`    // unix ms while running, 0 while paused
	Remaining int64  `json:"remaining"` // ms left while paused
	AskDone   bool   `json:"askDone"`   // last planned pomodoro of the task just finished
}

type Settings struct {
	WorkMin    int  `json:"workMin"`
	ShortMin   int  `json:"shortMin"`
	LongMin    int  `json:"longMin"`
	AutoBreaks bool `json:"autoBreaks"`
	AutoWork   bool `json:"autoWork"`
	Sound      bool `json:"sound"`
}

type State struct {
	Tasks      []Task         `json:"tasks"`
	Timer      Timer          `json:"timer"`
	Settings   Settings       `json:"settings"`
	Stats      map[string]int `json:"stats"` // YYYY-MM-DD -> pomodoros finished
	LastCredit int64          `json:"lastCredit"`
}

func defaultState() State {
	return State{
		Tasks:    []Task{},
		Timer:    Timer{Phase: "idle"},
		Settings: Settings{WorkMin: 25, ShortMin: 5, LongMin: 15, AutoBreaks: true, Sound: true},
		Stats:    map[string]int{},
	}
}

func (s Settings) Validate() error {
	switch {
	case s.WorkMin < 1 || s.WorkMin > 180:
		return errors.New("focus length must be 1-180 minutes")
	case s.ShortMin < 1 || s.ShortMin > 60:
		return errors.New("short break must be 1-60 minutes")
	case s.LongMin < 1 || s.LongMin > 120:
		return errors.New("long break must be 1-120 minutes")
	}
	return nil
}

func (t Timer) Validate() error {
	switch t.Phase {
	case "idle", "work", "short", "long":
	default:
		return fmt.Errorf("unknown phase %q", t.Phase)
	}
	if t.Duration < 0 || t.EndsAt < 0 || t.Remaining < 0 {
		return errors.New("timer values must not be negative")
	}
	return nil
}

func (st *State) task(id string) *Task {
	for i := range st.Tasks {
		if st.Tasks[i].ID == id {
			return &st.Tasks[i]
		}
	}
	return nil
}

// Reorder puts the given ids first, in order; tasks not mentioned keep
// their relative order after them.
func (st *State) Reorder(ids []string) {
	byID := make(map[string]Task, len(st.Tasks))
	for _, t := range st.Tasks {
		byID[t.ID] = t
	}
	out := make([]Task, 0, len(st.Tasks))
	seen := map[string]bool{}
	for _, id := range ids {
		if t, ok := byID[id]; ok && !seen[id] {
			out = append(out, t)
			seen[id] = true
		}
	}
	for _, t := range st.Tasks {
		if !seen[t.ID] {
			out = append(out, t)
		}
	}
	st.Tasks = out
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Store keeps the whole state in memory and writes it to a JSON file on
// every change. It is a single-user app; the state stays small.
type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	st := defaultState()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if st.Tasks == nil {
		st.Tasks = []Task{}
	}
	if st.Stats == nil {
		st.Stats = map[string]int{}
	}
	if st.Settings.Validate() != nil {
		st.Settings = defaultState().Settings
	}
	if st.Timer.Validate() != nil {
		st.Timer = Timer{Phase: "idle"}
	}
	return &Store{path: path, state: st}, nil
}

func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state)
}

// Update applies fn to a copy of the state and persists it. If fn returns
// an error nothing changes.
func (s *Store) Update(fn func(*State) error) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return State{}, err
	}
	if err := s.write(next); err != nil {
		return State{}, err
	}
	s.state = next
	return clone(next), nil
}

func (s *Store) write(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func clone(st State) State {
	data, err := json.Marshal(st)
	if err != nil {
		panic(err)
	}
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}
