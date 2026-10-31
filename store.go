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

// Item kinds. The queue mixes tasks and breaks; the timer works through it
// top to bottom.
const (
	KindTask  = "task"
	KindBreak = "break"
)

type Item struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	Pomodoros int        `json:"pomodoros"`
	Completed int        `json:"completed"`
	Source    string     `json:"source"`
	BreakMin  int        `json:"breakMin,omitempty"`
	Done      bool       `json:"done"`
	CreatedAt time.Time  `json:"createdAt"`
	DoneAt    *time.Time `json:"doneAt,omitempty"`
}

func (it Item) IsBreak() bool { return it.Kind == KindBreak }

// Timer phases.
const (
	PhaseIdle  = "idle"
	PhaseWork  = "work"  // a pomodoro of a task
	PhaseBreak = "break" // a break item from the queue
)

type Timer struct {
	Phase     string `json:"phase"`
	ItemID    string `json:"itemId"`
	Duration  int64  `json:"duration"`  // ms
	EndsAt    int64  `json:"endsAt"`    // unix ms while running, 0 while paused
	Remaining int64  `json:"remaining"` // ms left while paused
}

func (t Timer) Running() bool { return t.Phase != PhaseIdle && t.EndsAt > 0 }

type Settings struct {
	WorkMin    int  `json:"workMin"`
	BreakMin   int  `json:"breakMin"` // default length of a break added to the queue
	AutoBreaks bool `json:"autoBreaks"`
	AutoWork   bool `json:"autoWork"`
	Sound      bool `json:"sound"`
}

type State struct {
	Items    []Item         `json:"items"`
	Timer    Timer          `json:"timer"`
	Settings Settings       `json:"settings"`
	Stats    map[string]int `json:"stats"` // YYYY-MM-DD -> pomodoros finished
}

func defaultState() State {
	return State{
		Items:    []Item{},
		Timer:    Timer{Phase: PhaseIdle},
		Settings: Settings{WorkMin: 25, BreakMin: 5, AutoBreaks: true, Sound: true},
		Stats:    map[string]int{},
	}
}

func (s Settings) Validate() error {
	switch {
	case s.WorkMin < 1 || s.WorkMin > 180:
		return errors.New("focus length must be 1-180 minutes")
	case s.BreakMin < 1 || s.BreakMin > 120:
		return errors.New("default break must be 1-120 minutes")
	}
	return nil
}

func (st *State) item(id string) *Item {
	for i := range st.Items {
		if st.Items[i].ID == id {
			return &st.Items[i]
		}
	}
	return nil
}

func (st *State) index(id string) int {
	for i := range st.Items {
		if st.Items[i].ID == id {
			return i
		}
	}
	return -1
}

func (st *State) remove(id string) bool {
	if i := st.index(id); i >= 0 {
		st.Items = append(st.Items[:i], st.Items[i+1:]...)
		return true
	}
	return false
}

func (st *State) insert(i int, it Item) {
	i = max(0, min(i, len(st.Items)))
	st.Items = append(st.Items[:i], append([]Item{it}, st.Items[i:]...)...)
}

// Open returns the queue: items not done yet, in order.
func (st *State) Open() []Item {
	var out []Item
	for _, it := range st.Items {
		if !it.Done {
			out = append(out, it)
		}
	}
	return out
}

// Reorder puts the given ids first, in order; items not mentioned keep
// their relative order after them.
func (st *State) Reorder(ids []string) {
	byID := make(map[string]Item, len(st.Items))
	for _, it := range st.Items {
		byID[it.ID] = it
	}
	out := make([]Item, 0, len(st.Items))
	seen := map[string]bool{}
	for _, id := range ids {
		if it, ok := byID[id]; ok && !seen[id] {
			out = append(out, it)
			seen[id] = true
		}
	}
	for _, it := range st.Items {
		if !seen[it.ID] {
			out = append(out, it)
		}
	}
	st.Items = out
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
		migrate(&st, data)
	}
	if st.Items == nil {
		st.Items = []Item{}
	}
	if st.Stats == nil {
		st.Stats = map[string]int{}
	}
	if st.Settings.BreakMin == 0 {
		st.Settings.BreakMin = 5
	}
	if st.Settings.Validate() != nil {
		st.Settings = defaultState().Settings
	}
	switch st.Timer.Phase {
	case PhaseWork, PhaseBreak:
		if st.item(st.Timer.ItemID) == nil {
			st.Timer = Timer{Phase: PhaseIdle}
		}
	default:
		st.Timer = Timer{Phase: PhaseIdle}
	}
	return &Store{path: path, state: st}, nil
}

// migrate carries tasks over from the first version, which stored them
// under "tasks" with no kind.
func migrate(st *State, data []byte) {
	var old struct {
		Tasks []Item `json:"tasks"`
	}
	if json.Unmarshal(data, &old) == nil && len(st.Items) == 0 {
		st.Items = old.Tasks
	}
	for i := range st.Items {
		if st.Items[i].Kind == "" {
			st.Items[i].Kind = KindTask
		}
	}
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
