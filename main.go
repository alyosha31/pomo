package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const usage = `pomo - a tiny terminal Pomodoro timer

Usage:
  pomo [MINUTES]       Run a live foreground timer (default: 25)
  pomo start [MINUTES] Start a timer and return immediately
  pomo status [--short]
  pomo toggle          Pause or resume
  pomo stop            Stop the timer
  pomo help
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pomo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runForeground(25 * time.Minute)
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "start":
		d, err := durationArg(args[1:])
		if err != nil {
			return err
		}
		s := newRunningState(d, time.Now())
		if err := saveState(s); err != nil {
			return err
		}
		fmt.Printf("Focus started: %s\n", clock(d))
		return nil
	case "status":
		short := len(args) == 2 && args[1] == "--short"
		if len(args) > 2 || (len(args) == 2 && !short) {
			return errors.New("usage: pomo status [--short]")
		}
		return printStatus(short)
	case "toggle":
		return toggleTimer()
	case "stop":
		if err := deleteState(); err != nil {
			return err
		}
		fmt.Println("Timer stopped.")
		return nil
	default:
		d, err := parseMinutes(args[0])
		if err != nil || len(args) != 1 {
			return errors.New("unknown command; run 'pomo help'")
		}
		return runForeground(d)
	}
}

func durationArg(args []string) (time.Duration, error) {
	if len(args) == 0 {
		return 25 * time.Minute, nil
	}
	if len(args) != 1 {
		return 0, errors.New("usage: pomo start [MINUTES]")
	}
	return parseMinutes(args[0])
}

func parseMinutes(value string) (time.Duration, error) {
	minutes, err := strconv.ParseFloat(value, 64)
	if err != nil || minutes <= 0 {
		return 0, errors.New("minutes must be a positive number")
	}
	d := time.Duration(minutes * float64(time.Minute))
	if d < time.Second {
		return 0, errors.New("duration must be at least one second")
	}
	return d, nil
}

func printStatus(short bool) error {
	s, err := loadState()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	remaining := s.remaining(time.Now())
	if remaining <= 0 {
		if short {
			fmt.Print("🍅 done")
		} else {
			fmt.Println("Focus complete!")
		}
		return nil
	}
	label := clock(remaining)
	if short {
		if s.Paused {
			fmt.Printf("⏸ %s", label)
		} else {
			fmt.Printf("🍅 %s", label)
		}
		return nil
	}
	state := "focusing"
	if s.Paused {
		state = "paused"
	}
	fmt.Printf("%s — %s remaining\n", state, label)
	return nil
}

func toggleTimer() error {
	s, err := loadState()
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no active timer")
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if s.remaining(now) <= 0 {
		return errors.New("timer is already complete")
	}
	if s.Paused {
		s.End = now.Add(time.Duration(s.Remaining)).UnixNano()
		s.Remaining = 0
		s.Paused = false
		fmt.Println("Timer resumed.")
	} else {
		s.Remaining = int64(s.remaining(now))
		s.Paused = true
		fmt.Println("Timer paused.")
	}
	return saveState(s)
}

func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int64((d + time.Second - 1) / time.Second)
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
