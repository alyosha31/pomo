package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	st := State{Items: []Item{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}}
	st.Reorder([]string{"c", "a", "zz", "c"})
	var ids []string
	for _, it := range st.Items {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, "") != "cabd" {
		t.Errorf("got %v", ids)
	}
}

func TestStorePersistsAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := `{"tasks":[{"id":"x","title":"Old task","pomodoros":2}],"timer":{"phase":"long"},"settings":{"workMin":30,"shortMin":5,"longMin":15}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if len(st.Items) != 1 || st.Items[0].Kind != KindTask || st.Timer.Phase != PhaseIdle || st.Settings.WorkMin != 30 || st.Settings.BreakMin != 5 {
		t.Fatalf("migration: %+v", st)
	}
	if _, err := s.Update(func(st *State) error {
		st.Items = nil
		return errors.New("nope")
	}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.Update(func(st *State) error { st.Items[0].Title = "Saved"; return nil }); err != nil {
		t.Fatal(err)
	}
	s2, _ := OpenStore(path)
	if got := s2.Snapshot().Items; len(got) != 1 || got[0].Title != "Saved" {
		t.Errorf("got %+v", got)
	}
}

func queueState() *State {
	st := defaultState()
	st.Items = []Item{
		{ID: "a", Kind: KindTask, Title: "A", Pomodoros: 2},
		{ID: "b1", Kind: KindBreak, BreakMin: 10},
		{ID: "c", Kind: KindTask, Title: "C", Pomodoros: 1},
	}
	return &st
}

const minute = int64(60_000)

func TestTimerWalksTheQueue(t *testing.T) {
	st := queueState()
	if err := st.Toggle(0); err != nil {
		t.Fatal(err)
	}
	if st.Timer.Phase != PhaseWork || st.Timer.ItemID != "a" || st.Timer.EndsAt != 25*minute {
		t.Fatalf("start: %+v", st.Timer)
	}
	// first pomodoro of A ends: no automatic break, the next pomodoro of A
	// waits for Start (autoWork off)
	st.Advance(25 * minute)
	if st.Timer.Phase != PhaseWork || st.Timer.ItemID != "a" || st.Timer.Running() || st.Items[0].Completed != 1 {
		t.Fatalf("after pomodoro 1: %+v", st.Timer)
	}
	st.Toggle(31 * minute)
	// A's last pomodoro ends: A done, the queued break starts on its own
	st.Advance(56 * minute)
	if !st.Items[0].Done || st.Timer.Phase != PhaseBreak || st.Timer.ItemID != "b1" || st.Timer.Duration != 10*minute || !st.Timer.Running() {
		t.Fatalf("after A: %+v items %+v", st.Timer, st.Items)
	}
	// break ends: break removed, C loaded but not started
	st.Advance(66 * minute)
	if st.item("b1") != nil || st.Timer.ItemID != "c" || st.Timer.Running() {
		t.Fatalf("after break: %+v", st.Timer)
	}
	if st.Stats[time.UnixMilli(0).Format(time.DateOnly)] != 2 {
		t.Errorf("stats: %v", st.Stats)
	}
}

func TestAdvanceCatchesUpSeveralPhases(t *testing.T) {
	st := queueState()
	st.Settings.AutoWork = true
	st.Toggle(0)
	// everything runs on its own: A(25) A(25) break(10) C(25)
	st.Advance(200 * minute)
	if st.Timer.Phase != PhaseIdle || len(st.Open()) != 0 {
		t.Fatalf("expected the whole queue done: %+v %+v", st.Timer, st.Items)
	}
}

func TestSkipPauseAndLeaving(t *testing.T) {
	st := queueState()
	st.Toggle(0)
	st.Toggle(10 * minute) // pause
	if st.Timer.Running() || st.Timer.Remaining != 15*minute {
		t.Fatalf("pause: %+v", st.Timer)
	}
	st.Skip(11 * minute) // finishing early counts the pomodoro
	if st.Items[0].Completed != 1 || st.Timer.Phase != PhaseWork || st.Timer.Running() {
		t.Fatalf("skip: %+v %+v", st.Timer, st.Items[0])
	}
	// deleting the item the timer is on moves to the next one, unstarted
	st.leaving("a", 12*minute, func() { st.remove("a") })
	if st.Timer.ItemID != "b1" || st.Timer.Running() {
		t.Fatalf("leaving: %+v", st.Timer)
	}
	if err := st.FocusNow("c", 13*minute); err != nil || st.Items[0].ID != "c" || !st.Timer.Running() {
		t.Fatalf("focus now: %+v %+v", st.Timer, st.Items)
	}
}

func TestQueueSummary(t *testing.T) {
	st := queueState()
	sm := queueSummary(st, st.Open(), 0)
	// A: 2×25, break 10, C: 25
	if sm.Tasks != 2 || sm.Pomos != 3 || sm.Minutes != 85 {
		t.Errorf("got %+v", sm)
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

func newTestServer(t *testing.T, password string, p Planner) (*Server, http.Handler) {
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
	web, _ := fs.Sub(webFiles, "web")
	srv, err := NewServer(store, p, auth, web)
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.Handler()
}

func do(t *testing.T, h http.Handler, method, path, form string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form))
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuth(t *testing.T) {
	_, h := newTestServer(t, "hunter2", SimplePlanner{})
	if rec := do(t, h, "GET", "/board", ""); rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("unauthenticated board: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/static/app.css", ""); rec.Code != http.StatusOK {
		t.Fatalf("static files should be public: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/login", "password=wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec := do(t, h, "POST", "/login", "password=hunter2")
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("login: %d %v", rec.Code, cookies)
	}
	if rec := do(t, h, "GET", "/", "", cookies[0]); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="timer"`) {
		t.Fatalf("index: %d", rec.Code)
	}
	forged := &http.Cookie{Name: cookieName, Value: "99999999999.deadbeef"}
	if rec := do(t, h, "GET", "/board", "", forged); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie accepted: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/timer/toggle", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(cookies[0])
	cross := httptest.NewRecorder()
	h.ServeHTTP(cross, req)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-origin post: %d", cross.Code)
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

func TestPlanBreakDeleteFlow(t *testing.T) {
	srv, h := newTestServer(t, "", fakePlanner{tasks: []PlannedTask{{Title: "Deck", Pomodoros: 3}, {Title: "Dentist"}}})
	rec := do(t, h, "POST", "/plan", "text=deck+and+dentist")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Deck") || !strings.Contains(body, "2 tasks added") || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("plan: %d %s", rec.Code, body)
	}
	st := srv.store.Snapshot()
	if len(st.Items) != 2 || st.Items[0].Source != SourceAI || st.Items[1].Source != SourceDefault {
		t.Fatalf("items: %+v", st.Items)
	}
	deck, dentist := st.Items[0].ID, st.Items[1].ID

	do(t, h, "POST", "/items/"+deck+"/break-after", "")
	st = srv.store.Snapshot()
	if len(st.Items) != 3 || !st.Items[1].IsBreak() || st.Items[1].BreakMin != 5 {
		t.Fatalf("break-after: %+v", st.Items)
	}
	brk := st.Items[1].ID
	do(t, h, "POST", "/items/"+brk+"/minutes?delta=5", "")
	do(t, h, "POST", "/order", "id="+dentist+"&id="+brk+"&id="+deck)
	st = srv.store.Snapshot()
	if st.Items[0].ID != dentist || st.Items[1].BreakMin != 10 {
		t.Fatalf("order/minutes: %+v", st.Items)
	}

	do(t, h, "POST", "/items/"+deck+"/delete", "")
	do(t, h, "POST", "/items/"+dentist+"/done", "")
	st = srv.store.Snapshot()
	if len(st.Items) != 2 || !st.Items[0].Done || st.item(deck) != nil {
		t.Fatalf("delete/done: %+v", st.Items)
	}
	do(t, h, "POST", "/clear-done", "")
	if st = srv.store.Snapshot(); len(st.Items) != 1 || !st.Items[0].IsBreak() {
		t.Fatalf("clear done: %+v", st.Items)
	}
	if rec := do(t, h, "POST", "/items/nope/delete", ""); !strings.Contains(rec.Body.String(), "no longer exists") {
		t.Fatalf("missing item should toast: %s", rec.Body)
	}
}

func TestPlanFallsBackWhenAIFails(t *testing.T) {
	srv, h := newTestServer(t, "", fakePlanner{err: errors.New("not logged in")})
	rec := do(t, h, "POST", "/plan", "text=one%0Atwo")
	if !strings.Contains(rec.Body.String(), "AI planner failed") || len(srv.store.Snapshot().Items) != 2 {
		t.Fatalf("fallback: %d %s", rec.Code, rec.Body)
	}
}
