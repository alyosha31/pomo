# pomo

A tiny Pomodoro timer for the terminal and tmux.

```sh
go install github.com/alyosha31/pomo@latest
```

```sh
pomo                # live 40-minute timer
pomo start          # background 40-minute timer
pomo start 15       # custom duration
pomo toggle         # pause/resume
pomo stop
pomo status --short
```

Add the timer to `~/.tmux.conf`:

```tmux
set -g status-interval 1
set -g status-right '#(pomo status --short) | %H:%M'
bind-key p run-shell -b 'pomo start 40 >/dev/null 2>&1'
bind-key T command-prompt -p 'Pomodoro minutes:' -I '40' "run-shell -b 'pomo start %1 >/dev/null 2>&1'"
bind-key P run-shell -b 'pomo toggle >/dev/null 2>&1'
bind-key X run-shell -b 'pomo stop >/dev/null 2>&1'
```

Reload tmux with `tmux source-file ~/.tmux.conf`.

`p` replaces tmux's default previous-window binding; use `T` for a custom duration.
At zero, pomo rings the terminal bell and shows a tmux completion message once.
