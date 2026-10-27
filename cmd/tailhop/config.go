package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

type config struct {
	hostname, stateDir string
	port, maxClients   int
	idle               time.Duration
	level              slog.Level
}

// parseConfig performs no filesystem or network operations. Parse flag strings
// before validating environment fallbacks so explicit flags can replace invalid values.
func parseConfig(args []string, getenv func(string) string, output io.Writer) (config, error) {
	fs := flag.NewFlagSet("tailhop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := make(map[string]*string)
	for _, setting := range []struct{ flag, env, fallback, usage string }{
		{"hostname", "TS_HOSTNAME", "tailhop", "Tailscale hostname (DNS label; TS_HOSTNAME)"},
		{"port", "PROXY_PORT", "8080", "Tailnet TCP port, 1–65535 (PROXY_PORT)"},
		{"state-dir", "TS_STATE_DIR", "/var/lib/tailhop", "Private persistent tsnet state directory (TS_STATE_DIR)"},
		{"log-level", "LOG_LEVEL", "info", "debug, info, warn or error (LOG_LEVEL)"},
		{"max-clients", "MAX_CLIENTS", "100", "Positive global inbound connection limit (MAX_CLIENTS)"},
		{"idle-timeout", "IDLE_TIMEOUT", "10m", "Positive Go inactivity duration (IDLE_TIMEOUT)"},
	} {
		values[setting.env] = fs.String(setting.flag, setting.fallback, setting.usage)
	}
	fs.Usage = func() {
		fmt.Fprintln(output, "Usage: tailhop [flags]\nCLI flags > non-empty environment variables > defaults.\nTS_AUTHKEY is environment-only for initial enrollment; no .env files are loaded.")
		fs.SetOutput(output)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return config{}, err
		}
		return config{}, fmt.Errorf("invalid command-line flags; use --help for supported options")
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("positional arguments are not supported")
	}
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	keys := map[string]string{"TS_HOSTNAME": "hostname", "PROXY_PORT": "port", "TS_STATE_DIR": "state-dir", "LOG_LEVEL": "log-level", "MAX_CLIENTS": "max-clients", "IDLE_TIMEOUT": "idle-timeout"}
	value := func(key, fallback string) string {
		if explicit[keys[key]] {
			return *values[key]
		}
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	port, err := strconv.Atoi(value("PROXY_PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		return config{}, fmt.Errorf("--port / PROXY_PORT must be 1–65535")
	}
	maxClients, err := strconv.Atoi(value("MAX_CLIENTS", "100"))
	if err != nil || maxClients < 1 {
		return config{}, fmt.Errorf("--max-clients / MAX_CLIENTS must be a positive integer")
	}
	idle, err := time.ParseDuration(value("IDLE_TIMEOUT", "10m"))
	if err != nil || idle <= 0 {
		return config{}, fmt.Errorf("--idle-timeout / IDLE_TIMEOUT must be a positive Go duration")
	}
	var level slog.Level
	switch value("LOG_LEVEL", "info") {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return config{}, fmt.Errorf("--log-level / LOG_LEVEL must be debug, info, warn or error")
	}
	hostname := value("TS_HOSTNAME", "tailhop")
	if len(hostname) == 0 || len(hostname) > 63 || strings.Trim(hostname, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" || hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return config{}, fmt.Errorf("--hostname / TS_HOSTNAME must be a DNS label")
	}

	dir := value("TS_STATE_DIR", "/var/lib/tailhop")
	if dir == "" {
		return config{}, fmt.Errorf("--state-dir / TS_STATE_DIR must not be empty")
	}
	return config{hostname: hostname, stateDir: dir, port: port, maxClients: maxClients, idle: idle, level: level}, nil
}
