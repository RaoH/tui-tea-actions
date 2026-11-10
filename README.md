# tui-tea-actions

Terminal dashboard for Forgejo Actions runs across all repos you can see.

```sh
go install github.com/RaoH/tui-tea-actions@latest
# or from a clone
go build -o tui-tea-actions . && ./tui-tea-actions
```

The repo is private, so `go install` needs
`GOPRIVATE=github.com/RaoH/*` and git access to GitHub.

Credentials come from your default `tea` login. Override with
`-url`/`-token`, `FORGEJO_URL`/`FORGEJO_TOKEN`, or `-login <tea login name>`.

Flags: `-n` runs per repo (10), `-interval` refresh (15s, 0 = off),
`-owner` limit to one user/org, `-repos owner/a,owner/b` skip discovery,
`-notify` osc777 (default), osc9, bell or off, `-status` auto (default,
on in Rex), on or off, `-autowatch` off (default), mine or all, `-user`
login that counts as "mine" (default: the token's user).

Keys: `↑↓`/`jk` move, `enter`/`o` open run in browser, `w` watch run, `W` cycle autowatch, `a` repo actions
page, `p` repo page, `f`/`tab` cycle filter (all/active/failed),
`/` search, `esc` clear search, `r` refresh, `q` quit.

## Watching runs

Press `w` on a running or queued run to watch it (marked `◉`). When it
finishes, the app sends a desktop notification as an OSC 777 escape
sequence, the same mechanism Claude Code uses. Rex, iTerm2, Ghostty,
WezTerm and kitty show it as a system notification. Inside tmux the
sequence is wrapped for passthrough (needs `set -g allow-passthrough on`).

In Rex the tab also shows the watch state, like a Claude Code tab: `working`
while watched runs are in progress, then `done` (all passed) or `error` (any
failed) until you press a key in the app. This uses the OSC 7501 program
status sequence.

### Autowatch

`W` cycles autowatch: off → mine (runs you triggered) → all. New and
in-progress matching runs are watched automatically, including runs that
start and finish between two refreshes. Unwatching a run with `w` keeps it
unwatched. Several runs finishing in one refresh give one notification.
