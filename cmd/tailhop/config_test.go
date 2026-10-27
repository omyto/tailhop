package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
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
			want := config{tc.hostname, tc.dir, tc.port, tc.clients, tc.idle, tc.level}
			if cfg != want {
				t.Fatalf("got %+v want %+v", cfg, want)
			}
		})
	}
	for _, args := range [][]string{{"--hostname="}, {"--state-dir="}, {"--port=bad"}, {"--max-clients=0"}, {"--idle-timeout=0s"}, {"--log-level=trace"}, {"--auth-key=secret-value"}} {
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
