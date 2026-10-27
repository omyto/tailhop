package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	hostname, stateDir string
	authKey            string
	authKeyExplicit    bool
	port, maxClients   int
	idle               time.Duration
	level              slog.Level
}

// Retain the original flag error for callers while keeping its display safe.
type flagParseError struct {
	cause  error
	reason string
}

func (e flagParseError) Error() string {
	return "invalid command-line flags: " + e.reason + "; run 'tailhop --help' for usage"
}
func (e flagParseError) Unwrap() error { return e.cause }

// parseConfig performs no filesystem or network operations. Parse flag strings
// before validating environment fallbacks so explicit flags can replace invalid values.
func parseConfig(args []string, getenv func(string) string, output io.Writer) (config, error) {
	fs := flag.NewFlagSet("tailhop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := make(map[string]*string)
	for _, setting := range []struct{ flag, env, fallback, usage string }{
		{"auth-key", "TS_AUTHKEY", "", "Optional initial enrollment key (TS_AUTHKEY); visible in process arguments"},
		{"hostname", "TS_HOSTNAME", "tailhop", "Tailscale hostname (DNS label; TS_HOSTNAME)"},
		{"port", "PROXY_PORT", "8080", "Tailnet TCP port, 1–65535 (PROXY_PORT)"},
		{"state-dir", "TS_STATE_DIR", "", "Private persistent tsnet state directory (TS_STATE_DIR; default: absolute XDG_STATE_HOME/tailhop or $HOME/.local/state/tailhop)"},
		{"log-level", "LOG_LEVEL", "info", "debug, info, warn or error (LOG_LEVEL)"},
		{"max-clients", "MAX_CLIENTS", "100", "Positive global inbound connection limit (MAX_CLIENTS)"},
		{"idle-timeout", "IDLE_TIMEOUT", "10m", "Positive Go inactivity duration (IDLE_TIMEOUT)"},
	} {
		values[setting.env] = fs.String(setting.flag, setting.fallback, setting.usage)
	}
	// Keep flag diagnostics quiet; only explicit help prints usage.
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprintln(output, "Usage: tailhop [flags]\nCLI flags > non-empty environment variables > defaults.\nInteractive login by default; --auth-key or TS_AUTHKEY optionally enrolls a node.\nCLI secrets may appear in shell history, process listings and container metadata.\nNo configuration file is required; .env files are not loaded.")
			fs.SetOutput(output)
			fs.PrintDefaults()
			return config{}, err
		}
		reason := err.Error()
		// flag includes the entire offending token for malformed syntax. Omit
		// assigned values and redact the enrollment key even if used as a flag name.
		if strings.HasPrefix(reason, "bad flag syntax:") {
			reason, _, _ = strings.Cut(reason, "=")
		}
		reason = redactSecrets(reason, authSecrets(args, getenv)...)
		return config{}, flagParseError{cause: err, reason: reason}
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("positional arguments are not supported")
	}
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	keys := map[string]string{"TS_AUTHKEY": "auth-key", "TS_HOSTNAME": "hostname", "PROXY_PORT": "port", "TS_STATE_DIR": "state-dir", "LOG_LEVEL": "log-level", "MAX_CLIENTS": "max-clients", "IDLE_TIMEOUT": "idle-timeout"}
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

	dir := value("TS_STATE_DIR", "")
	if dir == "" && !explicit["state-dir"] {
		var err error
		dir, err = defaultStateDir(getenv, os.UserHomeDir)
		if err != nil {
			return config{}, err
		}
	}
	if dir == "" {
		return config{}, fmt.Errorf("--state-dir / TS_STATE_DIR must not be empty")
	}
	return config{authKey: value("TS_AUTHKEY", getenv("TS_AUTH_KEY")), authKeyExplicit: explicit["auth-key"], hostname: hostname, stateDir: dir, port: port, maxClients: maxClients, idle: idle, level: level}, nil
}

// Ignore relative XDG paths rather than making identity depend on the working directory.
func defaultStateDir(getenv func(string) string, homeDir func() (string, error)) (string, error) {
	if xdg := getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) && !strings.ContainsRune(xdg, 0) {
		return filepath.Join(xdg, "tailhop"), nil
	}
	home, err := homeDir()
	if err != nil || !filepath.IsAbs(home) || strings.ContainsRune(home, 0) {
		return "", fmt.Errorf("cannot resolve home directory for state; set --state-dir or TS_STATE_DIR")
	}
	return filepath.Join(home, ".local", "state", "tailhop"), nil
}

// Collect CLI keys even after an invalid flag so diagnostics cannot echo them.
func authSecrets(args []string, getenv func(string) string) []string {
	secrets := []string{getenv("TS_AUTHKEY"), getenv("TS_AUTH_KEY")}
	for i, arg := range args {
		name, value, assigned := strings.Cut(arg, "=")
		if name != "--auth-key" && name != "-auth-key" {
			continue
		}
		if assigned {
			secrets = append(secrets, value)
		} else if i+1 < len(args) {
			secrets = append(secrets, args[i+1])
		}
	}
	return secrets
}

func redactSecrets(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}
