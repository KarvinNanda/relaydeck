// Command relaydeck runs several Claude Code agents and lets them message each other.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

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

func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
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
	case "serve", "hook":
		fmt.Fprintf(stderr, "relaydeck %s: not implemented\n", cmd)
		return 1
	default:
		fmt.Fprintf(stderr, "relaydeck: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
