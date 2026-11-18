package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/auth"
	"github.com/KarvinNanda/relaydeck/internal/config"
	"github.com/KarvinNanda/relaydeck/internal/daemon"
	"github.com/KarvinNanda/relaydeck/internal/logx"
	"github.com/KarvinNanda/relaydeck/internal/server"
	"github.com/KarvinNanda/relaydeck/internal/webui"
)

const shutdownTimeout = 5 * time.Second

// serve runs the daemon until ctx ends (SIGINT or SIGTERM in main), then
// stops every agent and the HTTP server. Logs are JSON on stderr; stdout gets
// only the startup line with the UI token.
func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	portFlag := fs.Int("port", 0, "port to listen on (default: \"port\" in the config file)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "relaydeck serve: unexpected argument %q\n\n%s", fs.Arg(0), usage)
		return 2
	}
	if *portFlag != 0 && (*portFlag < 1024 || *portFlag > 65535) {
		fmt.Fprintln(stderr, "relaydeck serve: --port must be between 1024 and 65535")
		return 2
	}

	log := logx.New(stderr)
	// The delivery loop logs through slog.Default.
	slog.SetDefault(log)

	cfgPath, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(stderr, "relaydeck: find config dir: %v\n", err)
		return 1
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "relaydeck: %v\n", err)
		return 1
	}
	port := cfg.Port
	if *portFlag != 0 {
		port = *portFlag // this run only; the config file keeps its port
	}

	ln, err := server.Listen(port, cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer ln.Close()
	uiToken, err := auth.NewToken()
	if err != nil {
		fmt.Fprintf(stderr, "relaydeck: create token: %v\n", err)
		return 1
	}
	opts, err := daemon.DefaultOptions(cfgPath, cfg, port, log)
	if err != nil {
		fmt.Fprintf(stderr, "relaydeck: %v\n", err)
		return 1
	}
	d, err := daemon.New(opts)
	if err != nil {
		fmt.Fprintf(stderr, "relaydeck: %v\n", err)
		return 1
	}
	// The port is ours, so runtime dirs of a daemon that was killed are stale.
	daemon.CleanStaleRuntime(opts.RuntimeRoot, log)

	guard := &auth.Guard{Port: port, UIToken: uiToken, Agents: d.Tokens(), Tickets: auth.NewTicketStore(time.Now)}
	srv := &http.Server{
		Handler:           server.New(d, guard, webui.FS(), server.ZenityPicker{}, log),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	fmt.Fprintf(stdout, "relaydeck running: http://127.0.0.1:%d/#token=%s\n", port, uiToken)
	log.Info("relaydeck started", "port", port)

	// The daemon runs until ctx ends or the HTTP server fails. Run then stops
	// every agent while the server is still up, so their last hooks land.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := make(chan error, 1)
	go func() {
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			failed <- err
			cancel()
		}
	}()
	d.Run(runCtx)

	shCtx, shCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Warn("http shutdown", "error", err.Error())
	}
	select {
	case err := <-failed:
		log.Error("http server failed", "error", err.Error())
		return 1
	default:
	}
	log.Info("relaydeck stopped")
	return 0
}
