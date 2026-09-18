package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func runForeground(d time.Duration) error {
	s := newRunningState(d, time.Now())
	if err := saveState(s); err != nil {
		return err
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	defer fmt.Print("\033[?25h\n")
	fmt.Print("\033[?25l")

	for {
		remaining := s.remaining(time.Now())
		if remaining <= 0 {
			fmt.Print("\r\033[2K🍅 Focus complete!\a")
			return nil
		}
		fmt.Printf("\r\033[2K🍅 FOCUS  %s  (Ctrl+C to stop)", clock(remaining))
		select {
		case <-ticker.C:
		case <-interrupt:
			_ = deleteState()
			fmt.Print("\r\033[2KTimer stopped.")
			return nil
		}
	}
}
