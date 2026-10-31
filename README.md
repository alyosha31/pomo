# pomo

A self-hosted Pomodoro planner. Talk or type everything on your plate, and pomo turns it into tasks with pomodoro estimates, then runs focus and break sessions through them.

- **Brain dump → tasks.** Type everything on your plate (or dictate with a local tool like Handy). The text goes to the Claude Code CLI (or Codex) on the server, which splits it into tasks, orders them by any priorities you mentioned, and estimates pomodoros where it can tell. Tasks it can't size get a 1-pomodoro placeholder marked *set estimate*.
- **No API key.** It shells out to `claude -p` (or `codex exec`) on the server, so it runs on that machine's existing subscription login.
- **A queue of tasks and breaks.** Drag to reorder (mouse or touch). Add a break between tasks with **+ break**; it starts at 5 minutes, adjustable per break. The timer works through the queue top to bottom.
- **Timer.** 25 min of focus per pomodoro (adjustable). A task's pomodoros run back to back; the only breaks are the ones you put in the queue. The clock is green before it starts, yellow while running, red for the last minute.
- **Server-rendered.** Go `html/template` + htmx; the server owns the timer state, so a refresh or a second device picks up where you left off. One binary with templates and assets embedded; state is a single JSON file.

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

Then put it behind HTTPS (needed for notifications, and for the password to travel safely). With Caddy, use [`deploy/Caddyfile`](deploy/Caddyfile) with your domain, and point a DNS A record at the server.

The service runs as your user so it can reach `~/.claude` (or `~/.codex`). Check `claude -p hello` works as that user first.
