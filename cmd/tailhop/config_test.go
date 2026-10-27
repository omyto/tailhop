package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          []string
		env           map[string]string
		port, clients int
		wantErr       bool
	}{
		{name: "defaults", port: 8080, clients: 100},
		{name: "environment", env: map[string]string{"PROXY_PORT": "9090", "MAX_CLIENTS": "200"}, port: 9090, clients: 200},
		{name: "cli", args: []string{"--port=9090", "--max-clients=200"}, port: 9090, clients: 200},
		{name: "override", args: []string{"--max-clients=200"}, env: map[string]string{"MAX_CLIENTS": "invalid"}, port: 8080, clients: 200},
		{name: "explicit default", args: []string{"--port=8080"}, env: map[string]string{"PROXY_PORT": "9090"}, port: 8080, clients: 100},
		{name: "empty env", env: map[string]string{"PROXY_PORT": ""}, port: 8080, clients: 100},
		{name: "unknown", args: []string{"--unknown"}, wantErr: true},
		{name: "positional", args: []string{"extra"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg, err := parseConfig(tc.args, func(k string) string { return tc.env[k] }, &out)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: %v", err)
			}
			if err == nil && (cfg.port != tc.port || cfg.maxClients != tc.clients || cfg.hostname != "tailhop" || cfg.idle != 10*time.Minute) {
				t.Fatalf("config: %+v", cfg)
			}
		})
	}
	for key, values := range map[string][]string{
		"PROXY_PORT": {"0", "65536", "bad"}, "MAX_CLIENTS": {"0", "-1", "bad"}, "IDLE_TIMEOUT": {"0", "-1s", "bad"}, "LOG_LEVEL": {"trace"}, "TS_HOSTNAME": {"-bad", "bad-", "a.b", strings.Repeat("a", 64)},
	} {
		for _, v := range values {
			t.Run(key+v, func(t *testing.T) {
				_, err := parseConfig(nil, func(k string) string {
					if k == key {
						return v
					}
					return ""
				}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("error: %v", err)
				}
			})
		}
	}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer
			_, err := parseConfig([]string{arg}, func(string) string { t.Fatal("help read environment"); return "" }, &out)
			if !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "--") && !strings.Contains(out.String(), "-hostname") {
				t.Fatalf("help: %v %s", err, out.String())
			}
			t.Setenv("TS_STATE_DIR", t.TempDir()+"/must-not-exist")
			t.Setenv("PROXY_PORT", "invalid")
			old := osArgsForTest(arg)
			defer old()
			if err := run(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(os.Getenv("TS_STATE_DIR")); !os.IsNotExist(err) {
				t.Fatalf("help created state: %v", err)
			}
		})
	}
}

func osArgsForTest(arg string) func() {
	old := os.Args
	os.Args = []string{"tailhop", arg}
	return func() { os.Args = old }
}

func TestAllSettings(t *testing.T) {
	env := map[string]string{"TS_HOSTNAME": "env-node", "TS_STATE_DIR": "/env-state", "PROXY_PORT": "9090", "LOG_LEVEL": "error", "MAX_CLIENTS": "200", "IDLE_TIMEOUT": "15m"}
	args := []string{"--hostname=cli-node", "--state-dir=/cli-state", "--port=8080", "--log-level=info", "--max-clients=100", "--idle-timeout=10m"}
	for _, tc := range []struct {
		name          string
		args          []string
		hostname, dir string
		port, clients int
		idle          time.Duration
		level         slog.Level
	}{
		{"env", nil, "env-node", "/env-state", 9090, 200, 15 * time.Minute, slog.LevelError},
		{"cli overrides", args, "cli-node", "/cli-state", 8080, 100, 10 * time.Minute, slog.LevelInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfig(tc.args, func(k string) string { return env[k] }, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			want := config{hostname: tc.hostname, stateDir: tc.dir, port: tc.port, maxClients: tc.clients, idle: tc.idle, level: tc.level}
			if cfg != want {
				t.Fatalf("got %+v want %+v", cfg, want)
			}
		})
	}
	for _, args := range [][]string{{"--hostname="}, {"--state-dir="}, {"--port=bad"}, {"--max-clients=0"}, {"--idle-timeout=0s"}, {"--log-level=trace"}} {
		var out bytes.Buffer
		_, err := parseConfig(args, func(string) string { return "" }, &out)
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
		if strings.Contains(err.Error()+out.String(), "secret-value") {
			t.Fatal("exposed key")
		}
	}
}

func TestFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"unknown", []string{"--max-client", "200"}, "flag provided but not defined: -max-client"},
		{"missing", []string{"--hostname"}, "flag needs an argument: -hostname"},
		{"auth assignment", []string{"--auth-key=secret-value", "--max-client"}, "flag provided but not defined: -max-client"},
		{"auth separate", []string{"--auth-key", "secret-value", "--max-client"}, "flag provided but not defined: -max-client"},
		{"malformed assignment", []string{"---auth-key=secret-value"}, "bad flag syntax: ---auth-key"},
		{"secret as flag", []string{"--secret-value"}, "flag provided but not defined: -[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := parseConfig(tc.args, func(k string) string {
				if k == "TS_AUTHKEY" {
					return "secret-value"
				}
				return ""
			}, &out)
			if err == nil || !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), "tailhop --help") {
				t.Fatalf("unexpected error: %v", err)
			}
			if errors.Unwrap(err) == nil {
				t.Fatal("lost original flag error")
			}
			if out.Len() != 0 {
				t.Fatalf("unexpected usage output: %s", out.String())
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("exposed auth key")
			}
		})
	}
}

func TestCLIExitStatus(t *testing.T) {
	if os.Getenv("TAILHOP_TEST_CLI") == "1" {
		os.Args = []string{"tailhop", os.Getenv("TAILHOP_TEST_ARG")}
		main()
		os.Exit(0)
	}
	for _, arg := range []string{"--help", "-h", "--max-client=200", "--hostname", "--port=0"} {
		t.Run(arg, func(t *testing.T) {
			state := t.TempDir() + "/must-not-exist"
			cmd := exec.Command(os.Args[0], "-test.run=^TestCLIExitStatus$")
			cmd.Env = append(os.Environ(), "TAILHOP_TEST_CLI=1", "TAILHOP_TEST_ARG="+arg, "TS_AUTHKEY=secret-value", "TS_STATE_DIR="+state, "PROXY_PORT=invalid")
			out, err := cmd.CombinedOutput()
			help := arg == "--help" || arg == "-h"
			if (err == nil) != help {
				t.Fatalf("exit: %v; output: %s", err, out)
			}
			if !help {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("exit: %v", err)
				}
			}
			if strings.Contains(string(out), "secret-value") {
				t.Fatal("exposed key")
			}
			if strings.Contains(string(out), "Usage:") != help {
				t.Fatalf("unexpected usage output: %s", out)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("created state: %v", err)
			}
		})
	}
}

func TestAuthConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		env, want string
	}{
		{"cli", []string{"--auth-key=cli-secret"}, "", "cli-secret"},
		{"environment", nil, "env-secret", "env-secret"},
		{"precedence", []string{"--auth-key", "cli-secret"}, "env-secret", "cli-secret"},
		{"interactive", nil, "", ""},
		{"explicit empty", []string{"--auth-key="}, "env-secret", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg, err := parseConfig(tc.args, func(k string) string {
				if k == "TS_AUTHKEY" {
					return tc.env
				}
				return ""
			}, &out)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.authKey != tc.want {
				t.Fatal("incorrect authentication precedence")
			}
			if out.Len() != 0 {
				t.Fatal("parser printed configuration")
			}
			t.Setenv("TS_AUTHKEY", tc.env)
			if err := prepareAuth(cfg.authKey); err != nil {
				t.Fatal(err)
			}
			if cfg.authKey == "" && os.Getenv("TS_AUTHKEY") != "" {
				t.Fatal("tsnet would reuse overridden environment key")
			}
		})
	}
}

func TestAuthKeyDiagnostics(t *testing.T) {
	for _, args := range [][]string{
		{"--auth-key=cli-secret", "--help"}, {"--auth-key", "cli-secret", "-h"},
		{"--auth-key=cli-secret", "--cli-secret"},
		{"--cli-secret", "--auth-key=cli-secret"},
		{"---bad=cli-secret", "--auth-key", "cli-secret"},
		{"--auth-key=cli-secret", "--port=bad"},
		{"--auth-key"},
	} {
		var out bytes.Buffer
		_, err := parseConfig(args, func(k string) string {
			if k == "TS_AUTHKEY" {
				return "env-secret"
			}
			return ""
		}, &out)
		if err == nil {
			t.Fatal("expected help or validation error")
		}
		message := err.Error() + out.String()
		for _, secret := range []string{"cli-secret", "env-secret"} {
			if strings.Contains(message, secret) {
				t.Fatal("auth key leaked in diagnostic")
			}
		}
	}
	if got := redactSecrets("bad cli-secret and env-secret", "cli-secret", "env-secret"); strings.Contains(got, "secret") {
		t.Fatal("log redaction failed")
	}
}
