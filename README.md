# relaydeck

relaydeck is a local web app that runs several Claude Code agents at once, one per folder, and lets the agents message each other. It is a Go daemon with a Vue UI. Each agent is the real Claude Code TUI running in a terminal panel, so slash commands, `/resume`, skills, hooks and CLAUDE.md all work as usual.

Example: you tell the backend agent "when you are running, tell fe-admin and fe-customer". The agent calls a tool, and relaydeck delivers the message to the agents of those folders, starting them if needed.

Status: early development. Most commands are not implemented yet.

## Requirements

- relaydeck requires Claude Code installed and logged in on this machine. It starts your own `claude` binary. It never implements login and never reads, stores or forwards credentials or tokens. No API key is bundled.
- Go 1.27 or newer, to build from source.
- macOS is the main development platform.
- Windows (native) is **experimental**. It is designed in from the start but not tested yet. Claude Code inside WSL is not supported.
- Per-folder accounts through cswap are **untested** so far. They are disabled on Windows in v1.

## Build from source

```sh
go build -o relaydeck ./cmd/relaydeck
./relaydeck version
```

The Vue build is embedded from `internal/webui/dist`. That folder is empty until the web UI exists.

## Security

The daemon listens on `127.0.0.1` only, and every request carries a random token that is new on each start. No telemetry is sent anywhere.

## Policy note

Subscription login (Pro or Team) is used only through the official `claude` client. relaydeck is not a way to share or sell accounts.

## License

MIT, see `LICENSE`.
