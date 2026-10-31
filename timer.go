package main

import (
	"errors"
	"time"
)

// The timer walks the queue top to bottom. A task runs its pomodoros with
// short breaks between them, then is marked done; a break item runs once and
// is removed. Times are unix milliseconds so the browser can count down to
// the same instant.

func (st *State) phaseMs(phase string, it *Item) int64 {
	min := st.Settings.WorkMin
	switch phase {
	case PhaseShort:
		min = st.Settings.ShortMin
	case PhaseBreak:
		min = it.BreakMin
	}
	return int64(max(min, 1)) * 60_000
}

func (st *State) setPhase(phase string, it *Item, run bool, start int64) {
	d := st.phaseMs(phase, it)
	st.Timer = Timer{Phase: phase, ItemID: it.ID, Duration: d, Remaining: d}
	if run {
		st.Timer.EndsAt = start + d
	}
}

// load puts the timer on item it, starting it if run.
func (st *State) load(it *Item, run bool, at int64) {
	if it.IsBreak() {
		st.setPhase(PhaseBreak, it, run, at)
	} else {
		st.setPhase(PhaseWork, it, run, at)
	}
}

// next loads the head of the queue. With auto, it starts when the settings
// say that kind of phase starts on its own.
func (st *State) next(at int64, auto bool) {
	for i := range st.Items {
		it := &st.Items[i]
		if it.Done {
			continue
		}
		run := auto && (it.IsBreak() && st.Settings.AutoBreaks || !it.IsBreak() && st.Settings.AutoWork)
		st.load(it, run, at)
		return
	}
	st.Timer = Timer{Phase: PhaseIdle}
}

func (st *State) TimerItem() *Item {
	if st.Timer.Phase == PhaseIdle {
		return nil
	}
	return st.item(st.Timer.ItemID)
}

var errQueueEmpty = errors.New("the queue is empty — add a task first")

// Toggle starts the head of the queue, pauses, or resumes.
func (st *State) Toggle(now int64) error {
	t := &st.Timer
	switch {
	case t.Phase == PhaseIdle:
		st.next(now, false)
		if st.Timer.Phase == PhaseIdle {
			return errQueueEmpty
		}
		st.Timer.EndsAt = now + st.Timer.Remaining
	case t.Running():
		t.Remaining, t.EndsAt = max(0, t.EndsAt-now), 0
	default:
		t.EndsAt = now + t.Remaining
	}
	return nil
}

// finish ends the current phase at time at and moves on. credit is false
// for a skip, which does not count the pomodoro.
func (st *State) finish(at int64, credit bool) {
	it := st.TimerItem()
	if it == nil {
		st.next(at, false)
		return
	}
	switch st.Timer.Phase {
	case PhaseWork:
		if credit {
			it.Completed++
			st.Stats[time.UnixMilli(at).Format(time.DateOnly)]++
		}
		if it.Completed < it.Pomodoros {
			st.setPhase(PhaseShort, it, st.Settings.AutoBreaks, at)
			return
		}
		markDone(it, at)
	case PhaseShort:
		st.setPhase(PhaseWork, it, st.Settings.AutoWork, at)
		return
	case PhaseBreak:
		st.remove(it.ID)
	}
	st.next(at, true)
}

// Advance finishes every phase whose time has run out, so a timer left
// running while no page was open still ends up in the right place.
func (st *State) Advance(now int64) bool {
	changed := false
	for st.Timer.Running() && now >= st.Timer.EndsAt {
		st.finish(st.Timer.EndsAt, true)
		changed = true
	}
	return changed
}

func (st *State) Skip(now int64) {
	if st.Timer.Phase != PhaseIdle {
		st.finish(now, false)
	}
}

func (st *State) Reset() { st.Timer = Timer{Phase: PhaseIdle} }

// FocusNow moves an item to the top of the queue and starts it.
func (st *State) FocusNow(id string, now int64) error {
	i := st.index(id)
	if i < 0 || st.Items[i].Done {
		return errNoItem
	}
	it := st.Items[i]
	st.remove(id)
	st.insert(0, it)
	st.load(&st.Items[0], true, now)
	return nil
}

func markDone(it *Item, at int64) {
	it.Done = true
	t := time.UnixMilli(at).UTC()
	it.DoneAt = &t
}

// leaving wraps removing an item or marking it done by hand: if the timer
// was on that item, it moves to the next one without starting.
func (st *State) leaving(id string, now int64, fn func()) {
	on := st.Timer.Phase != PhaseIdle && st.Timer.ItemID == id
	fn()
	if on {
		st.next(now, false)
	}
}
