# relaydeck

relaydeck is a local web app that runs several Claude Code agents at once, one per folder, and lets the agents message each other. It is a Go daemon with a Vue UI. Each agent is the real Claude Code TUI running in a terminal panel, so slash commands, `/resume`, skills, hooks and CLAUDE.md all work as usual.

**New here? Read [docs/guide.md](docs/guide.md).** It explains what relaydeck is, how it differs from the Claude Code CLI, and how to install and use it.

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

While the daemon runs, it also writes a page with that link to `open.html` in your user cache directory (mode 0600). `relaydeck open` checks that the daemon answers, then opens that file in your default browser, so you do not have to copy the link:

```sh
./relaydeck open
```

The daemon removes the page on a clean shutdown. Any process that runs as your user can read the page and get the token.

## Start at login (macOS)

A LaunchAgent can start the daemon at login. Save this as `~/Library/LaunchAgents/com.karvinnanda.relaydeck.plist` and replace `USER_HOME` with your home directory (launchd does not expand `~` in these keys). Create the log directory first and keep it private: `mkdir -p ~/Library/Logs/relaydeck && chmod 700 ~/Library/Logs/relaydeck`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.karvinnanda.relaydeck</string>
  <key>ProgramArguments</key><array><string>/bin/zsh</string><string>-lic</string><string>exec "$HOME/KV/relaydeck/relaydeck" serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>Umask</key><integer>63</integer>
  <key>StandardOutPath</key><string>USER_HOME/Library/Logs/relaydeck/serve.out</string>
  <key>StandardErrorPath</key><string>USER_HOME/Library/Logs/relaydeck/relaydeck.log</string>
</dict></plist>
```

It runs through `zsh -lic` because launchd starts jobs with a minimal PATH, and the agents need your PATH to find `claude`, `node` and `docker`. A login zsh (`-l`) reads `.zshenv`, `.zprofile` and `.zlogin`, but not `.zshrc`. Many setups add `~/.local/bin` (where `claude` lives), Homebrew and fnm or nvm in `.zshrc`, so `-i` is added: it runs `.zshrc` even without a TTY. The cost is that prompt frameworks in `.zshrc` may print warnings to the log. Check the result with `env -i HOME="$HOME" /bin/zsh -lic 'command -v claude'`. The binary path in `ProgramArguments` is `$HOME/KV/relaydeck/relaydeck`: update it if the repo moves.

`serve.out` holds the startup line, which contains the UI token. `Umask` 63 (octal 077) applies to files relaydeck creates, but launchd opens the two log files itself with the default mode (0644), so the 0700 log directory is what keeps them private. Also run `chmod 600` on both files once they exist. Use `relaydeck open` to get into the UI.

- Load: `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.karvinnanda.relaydeck.plist`
- Restart after a rebuild: `launchctl kickstart -k gui/$(id -u)/com.karvinnanda.relaydeck`
- Remove: `launchctl bootout gui/$(id -u)/com.karvinnanda.relaydeck`, then delete the plist.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the processes and ports, the auth boundary, the data model, the rules the schema does not enforce, the known limits and where to change things.

## Security

The daemon listens on `127.0.0.1` only, and every API, WebSocket and agent request carries a random token that is new on each start (only the static UI files are public). The daemon also checks the `Host` and `Origin` headers. No telemetry is sent anywhere.

## Policy note

Subscription login (Pro or Team) is used only through the official `claude` client. relaydeck is not a way to share or sell accounts.

## License

MIT, see `LICENSE`.
