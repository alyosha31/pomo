# pomo

A self-hosted Pomodoro planner. Talk or type everything on your plate, and pomo turns it into tasks with pomodoro estimates, then runs focus and break sessions through them.

- **Brain dump → tasks.** Dictate with the mic (Web Speech API) or type. The text goes to the Claude Code CLI (or Codex) on the server, which splits it into tasks, orders them by any priorities you mentioned, and estimates pomodoros where it can tell. Tasks it can't size get a 1-pomodoro placeholder marked *set estimate*.
- **No API key.** It shells out to `claude -p` (or `codex exec`) on the server, so it runs on that machine's existing subscription login.
- **Drag to reorder.** Works with mouse and touch. The timer always takes the top open task next.
- **Timer.** 25 min of focus, then a 5 min short break between pomodoros of the same task and a 15 min long break between tasks (all adjustable). When a task's last planned pomodoro finishes, it asks: *Mark done* or *+1 pomodoro*.
- **Stays in sync.** State lives on the server, so a refresh or a second device picks up the running timer. Chime and browser notification at the end of each phase.
- One Go binary with the frontend embedded. State is a single JSON file.

Keys: <kbd>Space</kbd> start/pause, <kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>Enter</kbd> plan the dump or save an edit, <kbd>Esc</kbd> cancel an edit.

## Run

```sh
go build -o pomo . && ./pomo           # http://127.0.0.1:8080
```

| Flag / env | Default | |
|---|---|---|
| `-addr` / `POMO_ADDR` | `127.0.0.1:8080` | listen address |
| `-data` / `POMO_DATA` | `~/.local/share/pomo/state.json` | state file |
| `-ai` / `POMO_AI` | `auto` | `claude`, `codex`, `none`, or `auto` (first CLI found on `PATH`) |
| `-model` / `POMO_AI_MODEL` | `sonnet` for claude | model passed to the CLI |
| `POMO_PASSWORD` | unset | require a login. **Set this whenever pomo is reachable from the internet** — the planner runs your AI CLI. |

If the AI CLI fails (not logged in, timeout), pomo falls back to splitting the text line by line and shows a warning.

## Deploy on a VPS

```sh
go build -trimpath -ldflags='-s -w' -o ~/.local/bin/pomo .
mkdir -p ~/.config/pomo && printf 'POMO_PASSWORD=%s\n' "$(openssl rand -base64 18)" > ~/.config/pomo/env && chmod 600 ~/.config/pomo/env
sudo cp deploy/pomo.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable --now pomo
```

Then put it behind HTTPS (needed for the mic and notifications). With Caddy, use [`deploy/Caddyfile`](deploy/Caddyfile) with your domain, and point a DNS A record at the server.

The service runs as your user so it can reach `~/.claude` (or `~/.codex`). Check `claude -p hello` works as that user first.
