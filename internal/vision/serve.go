package vision

// serve.go is the entry point for the `setfree vision-proxy` subcommand. It
// parses the flags the parent passes (--target, --idle-timeout,
// --parent-pipe), resolves the vision config the same way the launcher does
// (config + env + secrets), and runs the proxy until it's cancelled or the
// idle backstop fires.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mindsdb/setfree/internal/config"
	"github.com/mindsdb/setfree/internal/gateway"
	"github.com/mindsdb/setfree/internal/secrets"
)

// Run is the `setfree vision-proxy` subcommand. It returns the process exit
// code. It's invoked by internal/app.Run's dispatch, not meant to be typed by
// a person.
func Run(args []string) int {
	fs := flag.NewFlagSet("vision-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var target string
	var idleTimeoutSecs int
	var parentPipe bool
	fs.StringVar(&target, "target", "", "the real gateway base URL to forward requests to")
	fs.IntVar(&idleTimeoutSecs, "idle-timeout", int(defaultIdleTimeout.Seconds()), "idle backstop timeout in seconds")
	fs.BoolVar(&parentPipe, "parent-pipe", false, "exit when stdin closes; the launcher holds the other end")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "vision proxy:", err)
		return 2
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "vision proxy: --target is required")
		return 2
	}

	// Resolve the vision config from the same sources the launcher used, so
	// the proxy and the launch decision agree. The main gateway's URL/key are
	// the defaults for the vision endpoint when it isn't separately configured.
	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vision proxy:", err)
		return 1
	}
	settings, err := config.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vision proxy:", err)
		return 1
	}
	store := secrets.NewFileStore(dir)
	resolver := gateway.NewResolver(settings, store)
	resolved, err := resolver.Resolve("")
	if err != nil && !gateway.ErrNotConfigured(err) {
		fmt.Fprintln(os.Stderr, "vision proxy:", err)
		return 1
	}
	cfg := Resolve(settings, store, os.Getenv, config.GatewaySetting{BaseURL: resolved.Gateway.BaseURL}, resolved.Gateway.APIKey)
	cfg.IdleTimeout = time.Duration(idleTimeoutSecs) * time.Second
	if !cfg.Enabled {
		fmt.Fprintln(os.Stderr, "vision proxy: no vision model configured (set SETFREE_VISION_MODEL or [vision].model)")
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel on a terminating signal so the idle backstop isn't the only way
	// out; the parent also kills us, but a manual start should respond to
	// Ctrl-C too.
	go watchSignals(ctx, cancel)
	if parentPipe {
		go watchParent(os.Stdin, cancel)
	}

	if err := Serve(ctx, os.Stdout, target, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "vision proxy:", err)
		return 1
	}
	return 0
}

// watchParent calls leave once r reaches EOF. The launcher holds the write
// end of our stdin and never writes to it, so EOF means that end closed, and
// the OS closes it when the launcher dies however it dies (SIGKILL, OOM,
// TerminateProcess). That's what makes this work the same everywhere: Windows
// has no reparenting to watch for, and there's no parent-death signal common
// to the platforms SetFree builds for.
func watchParent(r io.Reader, leave func()) {
	_, _ = io.Copy(io.Discard, r)
	leave()
}
