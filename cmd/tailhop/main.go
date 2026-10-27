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
	return start(cfg)
}

func start(cfg config) error {
	port, maxClients, idle, level := cfg.port, cfg.maxClients, cfg.idle, cfg.level
	hostname, dir := cfg.hostname, cfg.stateDir
	var err error
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Keep verbose backend logs suppressed. User status messages may include
	// an interactive enrollment URL; operators must treat that URL as a secret.
	node := &tsnet.Server{
		Hostname: hostname,
		Dir:      dir,
		AuthKey:  os.Getenv("TS_AUTHKEY"),
		Logf:     func(string, ...any) {},
		UserLogf: func(format string, args ...any) {
			logger.Info("tsnet", "message", fmt.Sprintf(format, args...))
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
	p := proxy.New(logger, maxClients, idle)
	server := p.Server()
	served := make(chan error, 1)
	go func() { served <- server.Serve(p.Listener(listener)) }()
	logger.Info("proxy listening", "port", port)
	select {
	case <-ctx.Done():
	case err = <-served:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("listener stopped", "error", err)
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
func main() {
	if err := run(); err != nil {
		slog.Error("tailhop failed", "error", err)
		os.Exit(1)
	}
}
