package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tailhop/internal/proxy"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

func run() error {
	cfg, err := parseConfig(os.Args[1:], os.Getenv, os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	// Snapshot both environment keys before explicit-empty cleanup.
	secrets := authSecrets(os.Args[1:], os.Getenv)
	if err := start(cfg, secrets); err != nil {
		return errors.New(redactSecrets(err.Error(), secrets...))
	}
	return nil
}

func start(cfg config, secrets []string) error {
	port, maxClients, idle, level := cfg.port, cfg.maxClients, cfg.idle, cfg.level
	hostname, dir := cfg.hostname, cfg.stateDir
	var err error
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := prepareStateDir(dir); err != nil {
		return err
	}
	// Only explicit empty CLI authentication disables tsnet's environment fallbacks.
	if err := prepareAuth(cfg); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Keep verbose backend logs suppressed. User status messages may include
	// an interactive enrollment URL; operators must treat that URL as a secret.
	node := &tsnet.Server{
		Hostname: hostname,
		Dir:      dir,
		AuthKey:  cfg.authKey,
		Logf:     func(string, ...any) {},
		UserLogf: func(format string, args ...any) {
			logger.Info("tsnet", "message", redactSecrets(fmt.Sprintf(format, args...), secrets...))
		},
	}
	defer node.Close()
	// Listen initializes tsnet synchronously; enrollment proceeds in tsnet's
	// background workers. Do not call Close concurrently with initialization.
	listener, err := node.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("tsnet listener failed: %w", err)
	}
	defer listener.Close()
	localClient, err := node.LocalClient()
	if err != nil {
		return fmt.Errorf("tsnet status client failed: %w", err)
	}
	statusCtx, cancelStatus := context.WithCancel(ctx)
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		reportRunning(statusCtx, logger, localClient)
	}()
	defer func() {
		cancelStatus()
		<-statusDone
	}()
	p := proxy.New(logger, maxClients, idle)
	server := p.Server()
	served := make(chan error, 1)
	go func() { served <- server.Serve(p.Listener(listener)) }()
	logger.Info("proxy listening", "port", port)
	select {
	case <-ctx.Done():
	case err = <-served:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("listener stopped", "error", redactSecrets(err.Error(), secrets...))
		}
	}
	serveErr := err
	drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = p.Shutdown(drain, server); err != nil {
		logger.Info("shutdown deadline reached")
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		return fmt.Errorf("proxy listener failed: %w", serveErr)
	}
	return nil
}

// Watch the initial state as well as transitions: persisted nodes may already be
// running, while enrollment can pass through several intermediate states.
func reportRunning(ctx context.Context, logger *slog.Logger, client *local.Client) {
	watcher, err := client.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err == nil {
		defer watcher.Close()
		for {
			var n ipn.Notify
			n, err = watcher.Next()
			if err != nil || ctx.Err() != nil {
				break
			}
			if n.State != nil && *n.State == ipn.Running {
				logger.Info("tailnet ready", "state", "Running")
				return
			}
		}
	}
	if ctx.Err() == nil {
		// Backend errors may contain credentials; keep the failure diagnostic fixed.
		logger.Warn("tailnet status unavailable")
	}
}

func prepareAuth(cfg config) error {
	if cfg.authKeyExplicit && cfg.authKey == "" {
		for _, name := range []string{"TS_AUTHKEY", "TS_AUTH_KEY"} {
			if err := os.Unsetenv(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareStateDir secures existing state without deleting or migrating it.
func prepareStateDir(dir string) error {
	var err error
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create TS_STATE_DIR: %w", err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure TS_STATE_DIR: %w", err)
	}
	f, err := os.CreateTemp(dir, ".writable-")
	if err != nil {
		return fmt.Errorf("TS_STATE_DIR must be writable: %w", err)
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("tailhop failed", "error", err)
		os.Exit(1)
	}
}
