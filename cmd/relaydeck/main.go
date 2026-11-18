// Command relaydeck runs several Claude Code agents and lets them message each other.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/KarvinNanda/relaydeck/internal/hook"
	"github.com/KarvinNanda/relaydeck/internal/logx"
	"github.com/KarvinNanda/relaydeck/internal/mcpserver"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: relaydeck [command]

commands:
  serve [--port N]   run the daemon (default when no command is given)
  mcp                run the MCP server for one agent
  hook <Event>       report a Claude Code hook event
  version            print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// runHook always returns 0: a failing relaydeck must never break the user's Claude session.
func runHook(args []string, stdin io.Reader, stderr io.Writer) int {
	log := logx.New(stderr)
	if len(args) != 1 {
		log.Error("hook failed", "error", "usage: relaydeck hook <Event>")
		return 0
	}
	if err := hook.Run(args[0], stdin, os.Getenv, &http.Client{}); err != nil {
		log.Error("hook failed", "event", args[0], "error", err.Error())
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "mcp":
		if err := mcpserver.Run(context.Background(), os.Getenv, nil); err != nil {
			fmt.Fprintf(stderr, "relaydeck mcp: %v\n", err)
			return 1
		}
		return 0
	case "hook":
		return runHook(args, os.Stdin, stderr)
	case "serve":
		// A signal from shutdownSignals ends ctx; serve then stops every agent
		// and exits.
		ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer stop()
		releaseOnFirstSignal(ctx, stop)
		return serve(ctx, args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "relaydeck: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

// releaseOnFirstSignal calls stop once ctx ends. After stop, the next signal
// has its default action, so a second Ctrl-C force quits when the graceful
// shutdown hangs. It returns after the goroutine is started.
func releaseOnFirstSignal(ctx context.Context, stop func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		stop()
		close(done)
	}()
	return done
}
