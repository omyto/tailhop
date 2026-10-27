package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStateResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"cli", []string{"--state-dir=/cli"}, nil, "/cli"},
		{"environment", nil, map[string]string{"TS_STATE_DIR": "/env"}, "/env"},
		{"xdg", nil, map[string]string{"XDG_STATE_HOME": "/xdg"}, "/xdg/tailhop"},
		{"home", nil, nil, filepath.Join(home, ".local/state/tailhop")},
		{"relative xdg", nil, map[string]string{"XDG_STATE_HOME": "relative"}, filepath.Join(home, ".local/state/tailhop")},
		{"invalid xdg", nil, map[string]string{"XDG_STATE_HOME": "/bad\x00path"}, filepath.Join(home, ".local/state/tailhop")},
		{"precedence", []string{"--state-dir=/cli"}, map[string]string{"TS_STATE_DIR": "/env", "XDG_STATE_HOME": "/xdg"}, "/cli"},
		{"docker", nil, map[string]string{"TS_STATE_DIR": "/var/lib/tailhop"}, "/var/lib/tailhop"},
		{"systemd", []string{"--state-dir=/var/lib/tailhop"}, map[string]string{"TS_STATE_DIR": "/other"}, "/var/lib/tailhop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfig(tc.args, func(k string) string { return tc.env[k] }, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.stateDir != tc.want {
				t.Fatalf("got %q want %q", cfg.stateDir, tc.want)
			}
		})
	}
	for _, home := range []string{"", "relative"} {
		if _, err := defaultStateDir(func(string) string { return "" }, func() (string, error) { return home, nil }); err == nil {
			t.Fatal("accepted invalid home")
		}
	}
	if _, err := defaultStateDir(func(string) string { return "" }, func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Fatal("ignored home error")
	}
	// Explicit state must work even when no home is available.
	t.Setenv("HOME", "")
	if _, err := parseConfig([]string{"--state-dir=/explicit"}, func(string) string { return "" }, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareStateDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := prepareStateDir(dir); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(dir, "existing-state")
	if err := os.WriteFile(credential, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := prepareStateDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	data, err := os.ReadFile(credential)
	if err != nil || string(data) != "preserved" {
		t.Fatalf("state changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary probe left behind: %v %v", entries, err)
	}
	for _, path := range []string{"", filepath.Join(root, "bad\x00path"), credential, filepath.Join(credential, "child"), "/proc/tailhop-state-test"} {
		if err := prepareStateDir(path); err == nil {
			t.Fatalf("accepted invalid/unwritable path %q", path)
		}
	}
}
