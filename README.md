# pomo

A tiny Pomodoro timer for the terminal and tmux.

```sh
go install github.com/alyosha31/pomo@latest
```

```sh
pomo 25             # live timer
pomo start 25       # background timer
pomo toggle         # pause/resume
pomo stop
pomo status --short
```

Add the timer to `~/.tmux.conf`:

```tmux
set -g status-interval 1
set -g status-right '#(pomo status --short) | %H:%M'
bind-key p run-shell 'pomo start 25'
bind-key P run-shell 'pomo toggle'
bind-key X run-shell 'pomo stop'
```

Reload tmux with `tmux source-file ~/.tmux.conf`.
