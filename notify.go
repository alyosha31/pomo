package main

import (
	"os"
	"os/exec"
	"strings"
)

func notifyTmux() {
	if os.Getenv("TMUX") == "" {
		return
	}
	_ = exec.Command("tmux", "display-message", "-d", "5000", "🍅 Focus complete!").Run()

	out, err := exec.Command("tmux", "list-clients", "-F", "#{client_tty}").Output()
	if err != nil {
		return
	}
	for _, tty := range strings.Fields(string(out)) {
		file, err := os.OpenFile(tty, os.O_WRONLY, 0)
		if err != nil {
			continue
		}
		_, _ = file.Write([]byte{'\a'})
		_ = file.Close()
	}
}
