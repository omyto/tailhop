package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVersionSkipsEnvironment(t *testing.T) {
	cfg, err := parseConfig([]string{"--version"}, func(string) string {
		t.Fatal("version read environment configuration")
		return ""
	}, io.Discard)
	if err != nil || !cfg.showVersion {
		t.Fatalf("version config: %+v, %v", cfg, err)
	}
}

func TestVersionBuildMetadata(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "tailhop")
	revision := "0123456789abcdef0123456789abcdef01234567"
	build := exec.Command("go", "build", "-buildvcs=false", "-ldflags=-s -w -X main.version=v1.2.3 -X main.commit="+revision, "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build versioned binary: %v\n%s", err, out)
	}
	state := filepath.Join(t.TempDir(), "must-not-exist")
	command := exec.Command(binary, "--version")
	command.Env = append(os.Environ(), "PROXY_PORT=invalid", "TS_STATE_DIR="+state, "TS_AUTHKEY=credential-secret")
	out, err := command.CombinedOutput()
	want := "tailhop v1.2.3 (commit " + revision + "; " + runtime.Version() + ")\n"
	if err != nil || string(out) != want {
		t.Fatalf("version output: %v, %q; want %q", err, out, want)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("version created state: %v", err)
	}
}
