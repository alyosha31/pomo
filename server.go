package main

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	store   *Store
	planner Planner
	auth    *Auth
	web     fs.FS
	tmpl    *template.Template
	version string
	now     func() time.Time
}

func NewServer(store *Store, planner Planner, auth *Auth, web fs.FS) (*Server, error) {
	tmpl, err := parseTemplates(web)
	if err != nil {
		return nil, err
	}
	return &Server{
		store: store, planner: planner, auth: auth, web: web, tmpl: tmpl,
		version: strconv.FormatInt(time.Now().Unix(), 36),
		now:     time.Now,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(s.web, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheForever(http.FileServerFS(static))))
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", s.index)
	app.HandleFunc("GET /board", s.board)
	app.HandleFunc("POST /timer/toggle", s.timerAction(func(st *State, now int64) error { return st.Toggle(now) }))
	app.HandleFunc("POST /timer/skip", s.timerAction(func(st *State, now int64) error { st.Skip(now); return nil }))
	app.HandleFunc("POST /timer/reset", s.timerAction(func(st *State, now int64) error { st.Reset(); return nil }))
	app.HandleFunc("POST /timer/sync", s.timerAction(func(*State, int64) error { return nil }))
	app.HandleFunc("POST /plan", s.plan)
	app.HandleFunc("POST /items/new", s.newTask)
	app.HandleFunc("GET /items/{id}/edit", s.editForm)
	app.HandleFunc("POST /items/{id}", s.saveItem)
	app.HandleFunc("POST /items/{id}/done", s.toggleDone)
	app.HandleFunc("POST /items/{id}/delete", s.deleteItem)
	app.HandleFunc("POST /items/{id}/pomodoros", s.stepPomodoros)
	app.HandleFunc("POST /items/{id}/minutes", s.stepMinutes)
	app.HandleFunc("POST /items/{id}/focus", s.focus)
	app.HandleFunc("POST /items/{id}/break-after", s.breakAfter)
	app.HandleFunc("POST /order", s.order)
	app.HandleFunc("POST /clear-done", s.clearDone)
	app.HandleFunc("POST /settings", s.saveSettings)
	mux.Handle("/", s.requireAuth(app))

	return securityHeaders(sameOrigin(mux))
}

func cacheForever(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// asset URLs carry ?v=<server start>, so a deploy busts the cache
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site form posts. The session cookie is SameSite
// Lax already; this covers older browsers.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Authed(r) {
			if r.Header.Get("HX-Request") != "" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

type httpError struct{ msg string }

func (e httpError) Error() string { return e.msg }

var errNoItem = httpError{"that item no longer exists"}

// mutate applies fn (after catching the timer up) and answers with the
// board as out-of-band swaps. A user-facing error comes back as a toast.
func (s *Server) mutate(w http.ResponseWriter, fn func(st *State, now int64) error, o viewOpts) {
	now := s.now()
	st, err := s.store.Update(func(st *State) error {
		st.Advance(now.UnixMilli())
		return fn(st, now.UnixMilli())
	})
	if err != nil {
		var he httpError
		msg := err.Error()
		if !errors.As(err, &he) && err != errQueueEmpty {
			log.Printf("update: %v", err)
			msg = "could not save"
		}
		st = s.store.Snapshot()
		o.toasts = append(o.toasts, toast{"err", msg})
	}
	v := s.newView(st, now, o)
	v.OOB = true
	s.render(w, "board", v)
}

// current returns the state with the timer caught up, saving only when a
// phase actually ended.
func (s *Server) current() State {
	st := s.store.Snapshot()
	now := s.now().UnixMilli()
	if st.Timer.Running() && now >= st.Timer.EndsAt {
		if next, err := s.store.Update(func(st *State) error { st.Advance(now); return nil }); err == nil {
			return next
		}
	}
	return st
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.render(w, "page", s.newView(s.current(), s.now(), viewOpts{}))
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	v := s.newView(s.current(), s.now(), viewOpts{})
	v.OOB = true
	s.render(w, "board", v)
}

func (s *Server) timerAction(fn func(*State, int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.mutate(w, fn, viewOpts{}) }
}

/* ---------- auth ---------- */

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.auth.Authed(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login", map[string]string{"Version": s.version})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.auth.CheckPassword(r.FormValue("password")) {
		time.Sleep(time.Second) // blunt brute-force damper
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]string{"Version": s.version, "Error": "Wrong password"})
		return
	}
	s.auth.SetSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

/* ---------- planning ---------- */

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		s.mutate(w, func(*State, int64) error { return httpError{"type what's on your plate first"} }, viewOpts{})
		return
	}
	if len(text) > 20000 {
		s.mutate(w, func(*State, int64) error { return httpError{"keep it under 20,000 characters"} }, viewOpts{})
		return
	}

	snap := s.store.Snapshot()
	req := PlanRequest{Text: text, WorkMin: snap.Settings.WorkMin}
	for _, it := range snap.Open() {
		if !it.IsBreak() {
			req.Existing = append(req.Existing, it.Title)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	planner := s.planner.Name()
	planned, err := s.planner.Plan(ctx, req)
	var toasts []toast
	if err != nil {
		log.Printf("planner %s failed: %v", planner, err)
		toasts = append(toasts, toast{"warn", "AI planner failed, so the text was split line by line. (" + truncate(err.Error(), 160) + ")"})
		planner = "none"
		planned, _ = SimplePlanner{}.Plan(ctx, req)
	}

	fresh := map[string]bool{}
	s.mutate(w, func(st *State, now int64) error {
		if len(planned) == 0 {
			return httpError{"couldn't find any tasks in that"}
		}
		created := time.UnixMilli(now).UTC()
		for _, p := range planned {
			it := Item{ID: newID(), Kind: KindTask, Title: p.Title, Notes: p.Notes, Pomodoros: p.Pomodoros, Source: SourceAI, CreatedAt: created}
			if planner == "none" || it.Pomodoros == 0 {
				it.Pomodoros, it.Source = 1, SourceDefault
			}
			st.Items = append(st.Items, it)
			fresh[it.ID] = true
		}
		return nil
	}, viewOpts{fresh: fresh, toasts: append(toasts, toast{"", plural(len(planned), "task") + " added"})})
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

/* ---------- items ---------- */

func (s *Server) newTask(w http.ResponseWriter, r *http.Request) {
	id := newID()
	s.mutate(w, func(st *State, now int64) error {
		st.Items = append(st.Items, Item{ID: id, Kind: KindTask, Title: "New task", Pomodoros: 1, Source: SourceManual, CreatedAt: time.UnixMilli(now).UTC()})
		return nil
	}, viewOpts{editing: id, fresh: map[string]bool{id: true}})
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request) {
	st := s.store.Snapshot()
	it := st.item(r.PathValue("id"))
	if it == nil || it.IsBreak() {
		s.board(w, r)
		return
	}
	iv := s.itemView(&st, *it, viewOpts{editing: it.ID})
	s.render(w, "item", iv)
}

func (s *Server) saveItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	title := truncate(strings.TrimSpace(r.FormValue("title")), 140)
	notes := truncate(strings.TrimSpace(r.FormValue("notes")), 500)
	s.mutate(w, func(st *State, now int64) error {
		it := st.item(id)
		if it == nil {
			return errNoItem
		}
		if title == "" {
			return httpError{"title can't be empty"}
		}
		it.Title, it.Notes = title, notes
		return nil
	}, viewOpts{})
}

func (s *Server) toggleDone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mutate(w, func(st *State, now int64) error {
		it := st.item(id)
		if it == nil {
			return errNoItem
		}
		st.leaving(id, now, func() {
			switch {
			case it.IsBreak():
				st.remove(id)
			case it.Done:
				it.Done, it.DoneAt = false, nil
			default:
				markDone(it, now)
			}
		})
		return nil
	}, viewOpts{})
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mutate(w, func(st *State, now int64) error {
		if st.item(id) == nil {
			return errNoItem
		}
		st.leaving(id, now, func() { st.remove(id) })
		return nil
	}, viewOpts{})
}

func delta(r *http.Request) int {
	d, _ := strconv.Atoi(r.URL.Query().Get("delta"))
	return d
}

func (s *Server) stepPomodoros(w http.ResponseWriter, r *http.Request) {
	id, d := r.PathValue("id"), delta(r)
	s.mutate(w, func(st *State, now int64) error {
		it := st.item(id)
		if it == nil || it.IsBreak() {
			return errNoItem
		}
		it.Pomodoros = max(1, min(it.Pomodoros+d, 50))
		it.Source = SourceManual
		return nil
	}, viewOpts{})
}

func (s *Server) stepMinutes(w http.ResponseWriter, r *http.Request) {
	id, d := r.PathValue("id"), delta(r)
	s.mutate(w, func(st *State, now int64) error {
		it := st.item(id)
		if it == nil || !it.IsBreak() {
			return errNoItem
		}
		it.BreakMin = max(1, min(it.BreakMin+d, 120))
		// a break that hasn't started yet picks up the new length
		if t := st.Timer; t.ItemID == id && !t.Running() && t.Remaining == t.Duration {
			st.load(it, false, now)
		}
		return nil
	}, viewOpts{})
}

func (s *Server) focus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mutate(w, func(st *State, now int64) error { return st.FocusNow(id, now) }, viewOpts{})
}

func (s *Server) breakAfter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	brk := Item{ID: newID(), Kind: KindBreak, Title: "Break", CreatedAt: s.now().UTC()}
	s.mutate(w, func(st *State, now int64) error {
		i := st.index(id)
		if i < 0 {
			return errNoItem
		}
		brk.BreakMin = st.Settings.BreakMin
		st.insert(i+1, brk)
		return nil
	}, viewOpts{fresh: map[string]bool{brk.ID: true}})
}

func (s *Server) order(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ids := r.Form["id"]
	s.mutate(w, func(st *State, now int64) error {
		st.Reorder(ids)
		// an idle timer always shows the new head; a loaded but unstarted
		// item follows the queue too
		if t := st.Timer; t.Phase == PhaseWork || t.Phase == PhaseBreak {
			if !t.Running() && t.Remaining == t.Duration {
				st.next(now, false)
			}
		}
		return nil
	}, viewOpts{})
}

func (s *Server) clearDone(w http.ResponseWriter, r *http.Request) {
	s.mutate(w, func(st *State, now int64) error {
		st.Items = st.Open()
		if st.Items == nil {
			st.Items = []Item{}
		}
		return nil
	}, viewOpts{})
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	num := func(k string) int { n, _ := strconv.Atoi(r.FormValue(k)); return n }
	set := Settings{
		WorkMin:    num("workMin"),
		ShortMin:   num("shortMin"),
		BreakMin:   num("breakMin"),
		AutoBreaks: r.FormValue("autoBreaks") != "",
		AutoWork:   r.FormValue("autoWork") != "",
		Sound:      r.FormValue("sound") != "",
	}
	s.mutate(w, func(st *State, now int64) error {
		if err := set.Validate(); err != nil {
			return httpError{err.Error()}
		}
		st.Settings = set
		// a loaded phase that hasn't started picks up the new length
		if t := st.Timer; t.Phase != PhaseIdle && !t.Running() && t.Remaining == t.Duration {
			if it := st.TimerItem(); it != nil {
				st.setPhase(t.Phase, it, false, now)
			}
		}
		return nil
	}, viewOpts{toasts: []toast{{"", "Settings saved"}}})
}
