# relaydeck

relaydeck is a local web app that runs several Claude Code agents at once, one per folder, and lets the agents message each other. It is a Go daemon with a Vue UI. Each agent is the real Claude Code TUI running in a terminal panel, so slash commands, `/resume`, skills, hooks and CLAUDE.md all work as usual.

Example: you tell the backend agent "when you are running, tell fe-admin and fe-customer". The agent calls a tool, and relaydeck delivers the message to the agents of those folders, starting them if needed.

Status: early development. The daemon, the agent messaging and the web UI (sidebar, terminal panels, Messages drawer) are built. The core flow (two real agents messaging each other, auto-start, nudge, read, hop limit, resume continuity) was verified end to end on macOS with Claude Code 2.1.327. Windows and per-folder cswap accounts are not verified.

## Requirements

- relaydeck requires Claude Code installed and logged in on this machine. It starts your own `claude` binary. It never implements login and never reads, stores or forwards credentials or tokens. No API key is bundled.
- Go 1.27 or newer and Node 24, to build from source.
- macOS is the main development platform.
- Windows (native) is **experimental**. It is designed in from the start but not tested yet. Claude Code inside WSL is not supported.
- Per-folder accounts through cswap are **untested** so far. They are disabled on Windows in v1.

## Build from source

```sh
cd web && npm ci && npm run build && cd ..
go build -o relaydeck ./cmd/relaydeck
./relaydeck version
```

`npm run build` writes the Vue UI into `internal/webui/dist`, and `go build` embeds it in the binary. Without the web build the binary still builds, but the UI answers 404.

## Run

```sh
./relaydeck serve
```

`relaydeck serve` listens on `127.0.0.1:7341` (change it with `--port N` or `port` in the config file) and prints a link like `http://127.0.0.1:7341/#token=...`. Open that link in a browser. The token is new on every start, so an old link or a bookmark stops working. The workspace and folder list is stored in `config.yaml` in your user config directory.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the processes and ports, the auth boundary, the data model, the rules the schema does not enforce, the known limits and where to change things.

## Security

The daemon listens on `127.0.0.1` only, and every API, WebSocket and agent request carries a random token that is new on each start (only the static UI files are public). The daemon also checks the `Host` and `Origin` headers. No telemetry is sent anywhere.

## Policy note

Subscription login (Pro or Team) is used only through the official `claude` client. relaydeck is not a way to share or sell accounts.

## License

MIT, see `LICENSE`.
