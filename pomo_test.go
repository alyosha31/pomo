package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestParsePlan(t *testing.T) {
	reply := "Here you go:\n```json\n{\"tasks\":[{\"title\":\"  Write report \",\"notes\":\"due Fri\",\"pomodoros\":3},{\"title\":\"\",\"pomodoros\":1},{\"title\":\"Call mom\",\"pomodoros\":40}]}\n```"
	got, err := parsePlan(reply)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlannedTask{{Title: "Write report", Notes: "due Fri", Pomodoros: 3}, {Title: "Call mom", Pomodoros: 12}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("task %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if _, err := parsePlan("sorry, no"); err == nil {
		t.Error("expected error for reply without JSON")
	}
}

func TestSimplePlanner(t *testing.T) {
	got, _ := SimplePlanner{}.Plan(context.Background(), PlanRequest{Text: "- email Sam\n2. fix bug; deploy.\n\n[ ] groceries"})
	var titles []string
	for _, p := range got {
		titles = append(titles, p.Title)
	}
	if strings.Join(titles, "|") != "email Sam|fix bug|deploy|groceries" {
		t.Errorf("got %q", titles)
	}
}

func TestClaudePlanner(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	p := &ClaudePlanner{Bin: "claude", run: func(_ context.Context, stdin, name string, args ...string) ([]byte, error) {
		gotArgs, gotStdin = args, stdin
		return []byte(`{"result":"{\"tasks\":[{\"title\":\"Ship it\",\"notes\":\"\",\"pomodoros\":2}]}","is_error":false}`), nil
	}}
	tasks, err := p.Plan(context.Background(), PlanRequest{Text: "ship it", WorkMin: 30, Existing: []string{"Old thing"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Title != "Ship it" || tasks[0].Pomodoros != 2 {
		t.Errorf("got %+v", tasks)
	}
	if !strings.Contains(gotStdin, "30 minutes") || !strings.Contains(gotStdin, "Old thing") {
		t.Errorf("prompt missing context:\n%s", gotStdin)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--tools ") || !strings.Contains(joined, "--model sonnet") {
		t.Errorf("args: %v", gotArgs)
	}

	p.run = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"result":"Not logged in","is_error":true}`), nil
	}
	if _, err := p.Plan(context.Background(), PlanRequest{Text: "x"}); err == nil {
		t.Error("expected error when claude reports is_error")
	}
}

func TestReorder(t *testing.T) {
	st := State{Tasks: []Task{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}}
	st.Reorder([]string{"c", "a", "zz", "c"})
	var ids []string
	for _, t := range st.Tasks {
		ids = append(ids, t.ID)
	}
	if strings.Join(ids, "") != "cabd" {
		t.Errorf("got %v", ids)
	}
}

func TestStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(st *State) error {
		st.Tasks = append(st.Tasks, Task{ID: "x", Title: "Persist me"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(st *State) error {
		st.Tasks = nil
		return errors.New("nope")
	}); err == nil {
		t.Fatal("expected error")
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Snapshot().Tasks; len(got) != 1 || got[0].Title != "Persist me" {
		t.Errorf("got %+v", got)
	}
}

type fakePlanner struct {
	tasks []PlannedTask
	err   error
}

func (fakePlanner) Name() string { return "fake" }
func (f fakePlanner) Plan(context.Context, PlanRequest) ([]PlannedTask, error) {
	return f.tasks, f.err
}

func newTestServer(t *testing.T, password string, p Planner) http.Handler {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(password, dir)
	if err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{"index.html": {Data: []byte("<html>")}}
	return (&Server{store: store, planner: p, auth: auth, web: web}).Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) State {
	t.Helper()
	var out struct{ State State }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out.State
}

func TestAuth(t *testing.T) {
	h := newTestServer(t, "hunter2", SimplePlanner{})
	if rec := do(t, h, "GET", "/api/state", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated state: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/", ""); rec.Code != http.StatusOK {
		t.Fatalf("static files should be public: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/login", `{"password":"wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec := do(t, h, "POST", "/api/login", `{"password":"hunter2"}`)
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("login: %d %v", rec.Code, cookies)
	}
	if rec := do(t, h, "GET", "/api/state", "", cookies[0]); rec.Code != http.StatusOK {
		t.Fatalf("authenticated state: %d", rec.Code)
	}
	forged := &http.Cookie{Name: cookieName, Value: "99999999999.deadbeef"}
	if rec := do(t, h, "GET", "/api/state", "", forged); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie accepted: %d", rec.Code)
	}
}

func TestTokenExpiry(t *testing.T) {
	a, err := NewAuth("pw", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok := a.newToken(now)
	if !a.validToken(tok, now) {
		t.Error("fresh token rejected")
	}
	if a.validToken(tok, now.Add(sessionTTL+time.Minute)) {
		t.Error("expired token accepted")
	}
}

func TestPlanAndPomodoroFlow(t *testing.T) {
	h := newTestServer(t, "", fakePlanner{tasks: []PlannedTask{{Title: "Deck", Pomodoros: 3}, {Title: "Dentist"}}})
	rec := do(t, h, "POST", "/api/plan", `{"text":"deck and dentist"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	st := decodeState(t, rec)
	if len(st.Tasks) != 2 || st.Tasks[0].Source != SourceAI || st.Tasks[1].Source != SourceDefault || st.Tasks[1].Pomodoros != 1 {
		t.Fatalf("tasks: %+v", st.Tasks)
	}
	id := st.Tasks[0].ID

	body := `{"taskId":"` + id + `","day":"2026-10-31","endsAt":123}`
	do(t, h, "POST", "/api/pomodoro", body)
	st = decodeState(t, do(t, h, "POST", "/api/pomodoro", body)) // duplicate from a second tab
	if st.Tasks[0].Completed != 1 || st.Stats["2026-10-31"] != 1 {
		t.Fatalf("pomodoro credited wrong: %+v %v", st.Tasks[0], st.Stats)
	}

	st = decodeState(t, do(t, h, "PATCH", "/api/tasks/"+id, `{"pomodoros":5,"done":true}`))
	if st.Tasks[0].Pomodoros != 5 || st.Tasks[0].Source != SourceManual || !st.Tasks[0].Done || st.Tasks[0].DoneAt == nil {
		t.Fatalf("patch: %+v", st.Tasks[0])
	}
	st = decodeState(t, do(t, h, "POST", "/api/clear-done", ""))
	if len(st.Tasks) != 1 || st.Tasks[0].Title != "Dentist" {
		t.Fatalf("clear done: %+v", st.Tasks)
	}
	if rec := do(t, h, "PUT", "/api/timer", `{"phase":"nap"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad phase accepted: %d", rec.Code)
	}
}

func TestPlanFallsBackWhenAIFails(t *testing.T) {
	h := newTestServer(t, "", fakePlanner{err: errors.New("not logged in")})
	rec := do(t, h, "POST", "/api/plan", `{"text":"one\ntwo"}`)
	var out struct {
		State   State
		Warning string
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || len(out.State.Tasks) != 2 || out.Warning == "" {
		t.Fatalf("fallback: %d %s", rec.Code, rec.Body)
	}
}
