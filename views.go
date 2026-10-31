package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"time"
)

func parseTemplates(web fs.FS) (*template.Template, error) {
	return template.New("").Funcs(template.FuncMap{
		"duration": fmtDuration,
	}).ParseFS(web, "templates/*.html")
}

type toast struct {
	Kind string // "", "warn", "err"
	Text string
}

// view is everything a page or fragment renders from.
type view struct {
	Timer        timerView
	Open         []itemView
	Done         []itemView
	Settings     Settings
	Today        int
	Summary      summary
	Toasts       []toast
	OOB          bool // render top-level fragments as htmx out-of-band swaps
	AuthRequired bool
	Planner      string
	Version      string
}

type itemView struct {
	Item
	Active  bool
	Fresh   bool
	Editing bool
	Last    bool
	Dots    []string
	Minutes int
}

type timerView struct {
	Phase      string
	Label      string
	Clock      string
	EndsAt     int64
	Remaining  int64
	Duration   int64
	Running    bool
	NotStarted bool
	Title      string
	Sub        string
	Toggle     string
	Sound      bool
}

type summary struct {
	Tasks   int
	Pomos   int
	Minutes int
}

type viewOpts struct {
	fresh   map[string]bool
	editing string
	toasts  []toast
}

func (s *Server) newView(st State, now time.Time, o viewOpts) view {
	nowMs := now.UnixMilli()
	v := view{
		Settings:     st.Settings,
		Today:        st.Stats[now.Format(time.DateOnly)],
		Toasts:       o.toasts,
		AuthRequired: s.auth.Enabled(),
		Planner:      s.planner.Name(),
		Version:      s.version,
		Timer:        newTimerView(&st, nowMs),
	}
	open := st.Open()
	for i, it := range open {
		iv := s.itemView(&st, it, o)
		iv.Last = i == len(open)-1
		v.Open = append(v.Open, iv)
	}
	for _, it := range st.Items {
		if it.Done {
			v.Done = append(v.Done, s.itemView(&st, it, o))
		}
	}
	v.Summary = queueSummary(&st, open, nowMs)
	return v
}

func (s *Server) itemView(st *State, it Item, o viewOpts) itemView {
	iv := itemView{
		Item:    it,
		Active:  st.Timer.Phase != PhaseIdle && st.Timer.ItemID == it.ID,
		Fresh:   o.fresh[it.ID],
		Editing: o.editing == it.ID,
		Minutes: it.BreakMin,
	}
	if !it.IsBreak() {
		iv.Minutes = it.Pomodoros * st.Settings.WorkMin
		for i := 0; i < max(it.Pomodoros, it.Completed); i++ {
			switch {
			case i >= it.Completed:
				iv.Dots = append(iv.Dots, "dot")
			case i >= it.Pomodoros:
				iv.Dots = append(iv.Dots, "dot extra")
			default:
				iv.Dots = append(iv.Dots, "dot full")
			}
		}
	}
	return iv
}

var phaseLabels = map[string]string{
	PhaseIdle:  "Ready",
	PhaseWork:  "Focus",
	PhaseBreak: "Break",
}

func newTimerView(st *State, now int64) timerView {
	t := st.Timer
	tv := timerView{Phase: t.Phase, Label: phaseLabels[t.Phase], EndsAt: t.EndsAt, Running: t.Running(), Sound: st.Settings.Sound}
	it := st.TimerItem()
	if it == nil {
		// idle: show what Start would pick up
		for _, o := range st.Open() {
			tv.Title = "Up next: " + o.Title
			if o.IsBreak() {
				tv.Title = fmt.Sprintf("Up next: %d min break", o.BreakMin)
			}
			break
		}
		if tv.Title == "" {
			tv.Title = "Nothing queued"
		}
		tv.Duration = int64(st.Settings.WorkMin) * 60_000
		tv.Remaining = tv.Duration
	} else {
		tv.Duration = t.Duration
		tv.Remaining = t.Remaining
		if tv.Running {
			tv.Remaining = max(0, t.EndsAt-now)
		}
		switch t.Phase {
		case PhaseWork:
			tv.Title = it.Title
			n := it.Completed + 1
			tv.Sub = fmt.Sprintf("pomodoro %d of %d", n, max(it.Pomodoros, n))
		case PhaseBreak:
			tv.Title = "Step away from the screen"
		}
	}
	tv.NotStarted = !tv.Running && tv.Remaining >= tv.Duration
	switch {
	case tv.Running:
		tv.Toggle = "Pause"
	case tv.NotStarted:
		tv.Toggle = "Start"
	default:
		tv.Toggle = "Resume"
	}
	tv.Clock = fmtClock(tv.Remaining)
	if t.Phase != PhaseIdle && !tv.Running && !tv.NotStarted {
		tv.Label += " · paused"
	}
	return tv
}

// queueSummary is the time left in the queue: pomodoros plus break items.
func queueSummary(st *State, open []Item, now int64) summary {
	var sm summary
	var ms int64
	work := int64(st.Settings.WorkMin) * 60_000
	for _, it := range open {
		if it.IsBreak() {
			ms += int64(it.BreakMin) * 60_000
			continue
		}
		left := max(it.Pomodoros-it.Completed, 1)
		sm.Tasks++
		sm.Pomos += left
		ms += int64(left) * work
	}
	// account for the phase in progress
	t := st.Timer
	rem := t.Remaining
	if t.Running() {
		rem = max(0, t.EndsAt-now)
	}
	switch t.Phase {
	case PhaseWork, PhaseBreak:
		ms -= t.Duration - rem
	}
	sm.Minutes = int(max(ms, 0) / 60_000)
	return sm
}

func fmtClock(ms int64) string {
	s := (ms + 999) / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func fmtDuration(min int) string {
	h, m := min/60, min%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
