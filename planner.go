package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// PlannedTask is what a planner extracts from a brain dump. Pomodoros is 0
// when the planner could not infer an estimate.
type PlannedTask struct {
	Title     string `json:"title"`
	Notes     string `json:"notes"`
	Pomodoros int    `json:"pomodoros"`
}

type PlanRequest struct {
	Text     string
	WorkMin  int
	Existing []string // titles of open tasks, so the planner skips duplicates
}

type Planner interface {
	Name() string
	Plan(ctx context.Context, req PlanRequest) ([]PlannedTask, error)
}

// runFunc runs a command with stdin and returns stdout. Swapped in tests.
type runFunc func(ctx context.Context, stdin string, name string, args ...string) ([]byte, error)

func runCommand(ctx context.Context, stdin string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return stdout.Bytes(), nil
}

// NewPlanner picks a planner by name: claude, codex, none, or auto (the
// first CLI found on PATH).
func NewPlanner(provider, model string) Planner {
	switch provider {
	case "claude":
		return &ClaudePlanner{Bin: "claude", Model: model, run: runCommand}
	case "codex":
		return &CodexPlanner{Bin: "codex", Model: model, run: runCommand}
	case "none":
		return SimplePlanner{}
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return NewPlanner("claude", model)
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return NewPlanner("codex", model)
	}
	return SimplePlanner{}
}

const systemPrompt = "You turn a person's messy brain dump into a clean list of Pomodoro tasks. You reply with a single JSON object and nothing else."

func buildPrompt(req PlanRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "One pomodoro is %d minutes of focused work.\n\n", req.WorkMin)
	b.WriteString(`Turn the brain dump below into discrete, actionable tasks.

Rules:
- One task per distinct piece of work. Merge duplicates. Split compound items ("do X and then Y") when they are separate pieces of work.
- title: short and imperative, at most 70 characters, in the user's language.
- notes: useful context from the dump for that task (deadlines, people, details), or "" if none. At most 200 characters.
- pomodoros: an integer from 1 to 12 estimating the focused work needed. Only estimate when the dump gives real signal: explicit durations ("an hour", "all afternoon"), size words ("quick", "huge"), or a task whose size is obvious from its nature. Use 0 when you genuinely cannot tell.
- Order tasks by priority if the user expressed any (deadlines, "first", "urgent", "most important"); otherwise keep the order they were mentioned.
- Leave out things that are not tasks (feelings, background) unless they add context to a task's notes.
`)
	if len(req.Existing) > 0 {
		b.WriteString("- These tasks are already on the list; do not repeat them:\n")
		for _, t := range req.Existing {
			fmt.Fprintf(&b, "  - %s\n", t)
		}
	}
	b.WriteString(`
Reply with exactly this shape: {"tasks":[{"title":"...","notes":"...","pomodoros":0}]}

Brain dump:
"""
`)
	b.WriteString(req.Text)
	b.WriteString("\n\"\"\"\n")
	return b.String()
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// parsePlan pulls the task list out of a model reply, tolerating code fences
// and chatter around the JSON object.
func parsePlan(reply string) ([]PlannedTask, error) {
	s := strings.TrimSpace(reply)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil, errors.New("planner reply had no JSON object")
	}
	var out struct {
		Tasks []PlannedTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil, fmt.Errorf("planner reply was not valid JSON: %w", err)
	}
	return cleanPlan(out.Tasks), nil
}

func cleanPlan(tasks []PlannedTask) []PlannedTask {
	out := make([]PlannedTask, 0, len(tasks))
	for _, t := range tasks {
		t.Title = truncate(strings.TrimSpace(t.Title), 140)
		t.Notes = truncate(strings.TrimSpace(t.Notes), 500)
		if t.Title == "" {
			continue
		}
		t.Pomodoros = max(0, min(t.Pomodoros, 12))
		out = append(out, t)
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ClaudePlanner uses the Claude Code CLI in print mode, so it runs on the
// machine's existing Claude login instead of an API key.
type ClaudePlanner struct {
	Bin   string
	Model string
	run   runFunc
}

func (p *ClaudePlanner) Name() string { return "claude" }

func (p *ClaudePlanner) Plan(ctx context.Context, req PlanRequest) ([]PlannedTask, error) {
	args := []string{
		"-p",
		"--output-format", "json",
		"--tools", "",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--system-prompt", systemPrompt,
	}
	model := p.Model
	if model == "" {
		model = "sonnet"
	}
	args = append(args, "--model", model)
	out, err := p.run(ctx, buildPrompt(req), p.Bin, args...)
	if err != nil {
		return nil, err
	}
	var res struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("unexpected claude output: %w", err)
	}
	if res.IsError {
		return nil, fmt.Errorf("claude: %s", truncate(res.Result, 300))
	}
	return parsePlan(res.Result)
}

// CodexPlanner uses the Codex CLI non-interactively with a read-only sandbox.
type CodexPlanner struct {
	Bin   string
	Model string
	run   runFunc
}

func (p *CodexPlanner) Name() string { return "codex" }

func (p *CodexPlanner) Plan(ctx context.Context, req PlanRequest) ([]PlannedTask, error) {
	dir, err := os.MkdirTemp("", "pomo-codex-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	last := filepath.Join(dir, "reply.txt")
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "-C", dir, "-o", last}
	if p.Model != "" {
		args = append(args, "-m", p.Model)
	}
	args = append(args, "-")
	if _, err := p.run(ctx, systemPrompt+"\n\n"+buildPrompt(req), p.Bin, args...); err != nil {
		return nil, err
	}
	reply, err := os.ReadFile(last)
	if err != nil {
		return nil, fmt.Errorf("codex left no reply: %w", err)
	}
	return parsePlan(string(reply))
}

// SimplePlanner splits the text into lines, bullets and semicolons. Used
// when no AI CLI is available and as a fallback when one fails.
type SimplePlanner struct{}

func (SimplePlanner) Name() string { return "none" }

var bulletRe = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)]|\[[ xX]?\])\s*`)

func (SimplePlanner) Plan(_ context.Context, req PlanRequest) ([]PlannedTask, error) {
	var tasks []PlannedTask
	for _, line := range strings.Split(req.Text, "\n") {
		for _, part := range strings.Split(line, ";") {
			title := strings.TrimSpace(bulletRe.ReplaceAllString(part, ""))
			title = strings.TrimRight(title, ".")
			if title != "" {
				tasks = append(tasks, PlannedTask{Title: title})
			}
		}
	}
	return cleanPlan(tasks), nil
}
