package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tailhop/internal/proxy"
	"tailscale.com/tsnet"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func run() error {
	port, err := strconv.Atoi(env("PROXY_PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("PROXY_PORT must be 1–65535")
	}
	maxClients, err := strconv.Atoi(env("MAX_CLIENTS", "100"))
	if err != nil || maxClients < 1 {
		return fmt.Errorf("MAX_CLIENTS must be a positive integer")
	}
	idle, err := time.ParseDuration(env("IDLE_TIMEOUT", "10m"))
	if err != nil || idle <= 0 {
		return fmt.Errorf("IDLE_TIMEOUT must be a positive Go duration")
	}
	var level slog.Level
	switch env("LOG_LEVEL", "info") {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
	}
	hostname := env("TS_HOSTNAME", "tailhop")
	if len(hostname) > 63 || strings.Trim(hostname, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" || hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return fmt.Errorf("TS_HOSTNAME must be a DNS label")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	dir := env("TS_STATE_DIR", "/var/lib/tailhop")
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
