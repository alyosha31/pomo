// Command pomo is a self-hosted Pomodoro planner: dump your tasks by voice
// or text, let Claude (or Codex) turn them into estimated tasks, then work
// through them with a focus/break timer.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed web
var webFiles embed.FS

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultDataPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "pomo", "state.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "pomo-state.json"
	}
	return filepath.Join(home, ".local", "share", "pomo", "state.json")
}

func main() {
	addr := flag.String("addr", env("POMO_ADDR", "127.0.0.1:8080"), "listen address (POMO_ADDR)")
	data := flag.String("data", env("POMO_DATA", defaultDataPath()), "state file (POMO_DATA)")
	provider := flag.String("ai", env("POMO_AI", "auto"), "planner: auto, claude, codex or none (POMO_AI)")
	model := flag.String("model", env("POMO_AI_MODEL", ""), "model passed to the planner CLI (POMO_AI_MODEL)")
	flag.Parse()
	password := os.Getenv("POMO_PASSWORD")

	store, err := OpenStore(*data)
	if err != nil {
		log.Fatal(err)
	}
	auth, err := NewAuth(password, filepath.Dir(*data))
	if err != nil {
		log.Fatal(err)
	}
	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	if !auth.Enabled() && !isLoopback(*addr) {
		log.Printf("WARNING: listening on %s with no POMO_PASSWORD; anyone who can reach it can run your AI CLI", *addr)
	}

	srv := &Server{store: store, planner: NewPlanner(*provider, *model), auth: auth, web: web}
	log.Printf("pomo on http://%s (planner: %s, data: %s)", *addr, srv.planner.Name(), *data)
	hs := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(hs.ListenAndServe())
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
