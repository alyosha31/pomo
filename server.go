package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	store   *Store
	planner Planner
	auth    *Auth
	web     fs.FS
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/state", s.getState)
	api.HandleFunc("POST /api/plan", s.plan)
	api.HandleFunc("POST /api/tasks", s.createTask)
	api.HandleFunc("PATCH /api/tasks/{id}", s.patchTask)
	api.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	api.HandleFunc("PUT /api/order", s.putOrder)
	api.HandleFunc("POST /api/clear-done", s.clearDone)
	api.HandleFunc("PUT /api/timer", s.putTimer)
	api.HandleFunc("POST /api/pomodoro", s.creditPomodoro)
	api.HandleFunc("PUT /api/settings", s.putSettings)
	mux.Handle("/api/", s.requireAuth(api))

	mux.Handle("/", http.FileServerFS(s.web))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "microphone=(self)")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Authed(r) {
			writeError(w, http.StatusUnauthorized, "log in first")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

// update runs fn against the store and replies with the new state.
func (s *Server) update(w http.ResponseWriter, fn func(*State) error) {
	st, err := s.store.Update(fn)
	if err != nil {
		var he httpError
		if errors.As(err, &he) {
			writeError(w, he.status, he.msg)
			return
		}
		log.Printf("update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": st})
}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func badRequest(msg string) error { return httpError{http.StatusBadRequest, msg} }

var errNoTask = httpError{http.StatusNotFound, "task not found"}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"authed":       s.auth.Authed(r),
		"authRequired": s.auth.Enabled(),
		"planner":      s.planner.Name(),
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.auth.Enabled() {
		writeJSON(w, http.StatusOK, map[string]bool{"authed": true})
		return
	}
	if !s.auth.CheckPassword(body.Password) {
		time.Sleep(time.Second) // blunt brute-force damper
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.auth.SetSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"authed": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]bool{"authed": false})
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.Snapshot()})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "say or type what's on your plate first")
		return
	}
	if len(text) > 20000 {
		writeError(w, http.StatusBadRequest, "that's a lot — keep it under 20,000 characters")
		return
	}

	snap := s.store.Snapshot()
	req := PlanRequest{Text: text, WorkMin: snap.Settings.WorkMin}
	for _, t := range snap.Tasks {
		if !t.Done {
			req.Existing = append(req.Existing, t.Title)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	planner, warning := s.planner.Name(), ""
	planned, err := s.planner.Plan(ctx, req)
	if err != nil {
		log.Printf("planner %s failed: %v", planner, err)
		warning = "AI planner failed, so the text was split line by line. (" + truncate(err.Error(), 160) + ")"
		planner = "none"
		planned, _ = SimplePlanner{}.Plan(ctx, req)
	}
	if len(planned) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "couldn't find any tasks in that")
		return
	}

	var added []string
	st, err := s.store.Update(func(st *State) error {
		now := time.Now().UTC()
		for _, p := range planned {
			t := Task{ID: newID(), Title: p.Title, Notes: p.Notes, Pomodoros: p.Pomodoros, Source: SourceAI, CreatedAt: now}
			if planner == "none" || t.Pomodoros == 0 {
				t.Pomodoros, t.Source = 1, SourceDefault
			}
			st.Tasks = append(st.Tasks, t)
			added = append(added, t.ID)
		}
		return nil
	})
	if err != nil {
		log.Printf("plan save: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": st, "added": added, "planner": planner, "warning": warning})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string `json:"title"`
		Notes     string `json:"notes"`
		Pomodoros int    `json:"pomodoros"`
		Completed int    `json:"completed"`
		Source    string `json:"source"`
		Index     *int   `json:"index"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s.update(w, func(st *State) error {
		title := truncate(strings.TrimSpace(body.Title), 140)
		if title == "" {
			return badRequest("title is required")
		}
		t := Task{
			ID:        newID(),
			Title:     title,
			Notes:     truncate(strings.TrimSpace(body.Notes), 500),
			Pomodoros: max(1, min(body.Pomodoros, 50)),
			Completed: max(0, body.Completed),
			Source:    SourceManual,
			CreatedAt: time.Now().UTC(),
		}
		if body.Source == SourceAI || body.Source == SourceDefault {
			t.Source = body.Source
		}
		i := len(st.Tasks)
		if body.Index != nil && *body.Index >= 0 && *body.Index < i {
			i = *body.Index
		}
		st.Tasks = append(st.Tasks[:i], append([]Task{t}, st.Tasks[i:]...)...)
		return nil
	})
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     *string `json:"title"`
		Notes     *string `json:"notes"`
		Pomodoros *int    `json:"pomodoros"`
		Completed *int    `json:"completed"`
		Done      *bool   `json:"done"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	s.update(w, func(st *State) error {
		t := st.task(id)
		if t == nil {
			return errNoTask
		}
		if body.Title != nil {
			title := truncate(strings.TrimSpace(*body.Title), 140)
			if title == "" {
				return badRequest("title can't be empty")
			}
			t.Title = title
		}
		if body.Notes != nil {
			t.Notes = truncate(strings.TrimSpace(*body.Notes), 500)
		}
		if body.Pomodoros != nil {
			t.Pomodoros = max(1, min(*body.Pomodoros, 50))
			t.Source = SourceManual
		}
		if body.Completed != nil {
			t.Completed = max(0, min(*body.Completed, 99))
		}
		if body.Done != nil && *body.Done != t.Done {
			t.Done = *body.Done
			t.DoneAt = nil
			if t.Done {
				now := time.Now().UTC()
				t.DoneAt = &now
			}
		}
		return nil
	})
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.update(w, func(st *State) error {
		for i, t := range st.Tasks {
			if t.ID == id {
				st.Tasks = append(st.Tasks[:i], st.Tasks[i+1:]...)
				return nil
			}
		}
		return errNoTask
	})
}

func (s *Server) putOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s.update(w, func(st *State) error {
		st.Reorder(body.IDs)
		return nil
	})
}

func (s *Server) clearDone(w http.ResponseWriter, r *http.Request) {
	s.update(w, func(st *State) error {
		open := st.Tasks[:0]
		for _, t := range st.Tasks {
			if !t.Done {
				open = append(open, t)
			}
		}
		st.Tasks = open
		return nil
	})
}

func (s *Server) putTimer(w http.ResponseWriter, r *http.Request) {
	var t Timer
	if !readJSON(w, r, &t) {
		return
	}
	s.update(w, func(st *State) error {
		if err := t.Validate(); err != nil {
			return badRequest(err.Error())
		}
		st.Timer = t
		return nil
	})
}

// creditPomodoro records a finished focus session. EndsAt identifies the
// session, so two open tabs finishing the same one only count it once.
func (s *Server) creditPomodoro(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskID string `json:"taskId"`
		Day    string `json:"day"`
		EndsAt int64  `json:"endsAt"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, err := time.Parse(time.DateOnly, body.Day); err != nil {
		writeError(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
		return
	}
	s.update(w, func(st *State) error {
		if body.EndsAt != 0 && body.EndsAt == st.LastCredit {
			return nil
		}
		if t := st.task(body.TaskID); t != nil {
			t.Completed++
		}
		st.Stats[body.Day]++
		st.LastCredit = body.EndsAt
		return nil
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var set Settings
	if !readJSON(w, r, &set) {
		return
	}
	s.update(w, func(st *State) error {
		if err := set.Validate(); err != nil {
			return badRequest(err.Error())
		}
		st.Settings = set
		return nil
	})
}
