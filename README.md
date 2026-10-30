# Claude Cockpit

A terminal cockpit for running and watching several Claude Code sessions at
once – on Windows and Linux.

The cockpit starts Claude Code headless (`claude -p --input-format
stream-json --output-format stream-json`) and renders the conversation like
the Claude Code terminal: messages, tool calls with their output, diffs with
line numbers, the thinking text, turn summaries and permission denials.
Sessions running in a normal Claude terminal can be followed as well.

## Features

- **Sessions**: start, stop, resume, interrupt (`esc` / `ctrl+c`), model and
  effort per session, grouped by repository in the session list.
- **git worktrees**: a new session can work directly in the repository, in a
  new worktree with its own branch (any name, next to the repository in
  `<repo>-worktrees/<name>`) or in an existing worktree/branch.
- **Split view**: 1, 2 side by side, 2 stacked, 3 or 4 sessions at once.
- **Tabs** per session: History, Files (tree with git status, live updates,
  preview), Notes, Pins (pin any history entry and jump back to it).
- **IDE**: open the project, a file or a diff (against HEAD) in IntelliJ or
  VS Code.
- **Usage**: 5-hour and 7-day limits with reset times, context size per
  session.
- **Commands**: `/` lists the cockpit's commands and Claude Code's own slash
  commands (marked `claude ▸`, sent to Claude).
- **Observer mode**: follow a session that runs in a real Claude terminal.

## Build and run

```sh
go build -o cockpit.exe ./cmd/cockpit       # Windows
GOOS=linux go build -o cockpit ./cmd/cockpit
./cockpit.exe
```

Requires `claude` (Claude Code) and `git` in the PATH. `dev.cmd` / `dev.ps1`
rebuild and restart the cockpit in a loop while developing.

## Keys

The message field always has the focus, like in Claude Code.

| Key | Action |
|---|---|
| `enter` / `shift+enter` | send / new line (also `alt+enter`, `ctrl+j`, `\` at the end) |
| `/` or `ctrl+p` | command list |
| `?` | help |
| `esc`, `ctrl+c` | clear the text or interrupt Claude; `ctrl+c` twice quits |
| `ctrl+k` | search and switch sessions |
| `ctrl+n` | new session |
| `alt+1` … `alt+4` | tabs History, Files, Notes, Pins |
| `ctrl+o` | view: normal → all → compact |
| `ctrl+\` | split view layouts |
| `alt+arrows` | previous/next session, in split view: move between panes |
| `tab` | move between message field, right panel and session list |

In the history: `y` copy, `p` pin, `o` open the file in the IDE, `d` diff in
the IDE, `enter` expand.

## Configuration

`/settings` edits the configuration; it is stored in `state.json` in the user
config folder (`%AppData%\llm-cockpit` or `~/.config/llm-cockpit`):

```json
{
  "config": {
    "claude_bin": "claude",
    "permission_mode": "acceptEdits",
    "ide": "idea",
    "thinking_display": "summarized",
    "session_link": "https://company.atlassian.net/browse/",
    "extra_args": []
  }
}
```

- `ide`: command that opens a folder or file (`idea`, `idea64`, `code`, …);
  IntelliJ is found automatically if `idea` is not in the PATH.
- `session_link`: `/link` opens this URL with the session name appended (or
  replacing `{name}`).
- `COCKPIT_DEBUG=1` logs key and mouse events to `%TEMP%\llm-cockpit-debug.log`.

## Layout of the code

- `internal/core` – sessions, Claude processes, stream-json, transcripts,
  observer mode, state (independent of the UI)
- `internal/platform` – git, worktrees, file watcher, clipboard, IDE,
  browser, terminal
- `internal/ui` – Bubble Tea frontend

## Tests

```sh
go test ./...
COCKPIT_LIVE=1 go test ./internal/core -run Live   # against the real Claude CLI
```
