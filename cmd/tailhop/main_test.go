package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/net/memnet"
)

func TestResourceConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"MAX_CLIENTS", "0"}, {"MAX_CLIENTS", "-1"}, {"MAX_CLIENTS", "invalid"},
		{"IDLE_TIMEOUT", "0"}, {"IDLE_TIMEOUT", "-1s"}, {"IDLE_TIMEOUT", "invalid"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			t.Setenv("PROXY_PORT", "8080")
			t.Setenv("MAX_CLIENTS", "100")
			t.Setenv("IDLE_TIMEOUT", "10m")
			t.Setenv(tc.key, tc.value)
			if _, err := parseConfig(nil, os.Getenv, io.Discard); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("invalid configuration: %v", err)
			}
		})
	}
}

func statusClient(t *testing.T, handler http.HandlerFunc) *local.Client {
	t.Helper()
	listener := memnet.Listen("local-tailscaled.sock:80")
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/watch-ipn-bus" || r.URL.Query().Get("mask") != "2" {
			t.Errorf("unexpected status request: %s", r.URL)
		}
		handler(w, r)
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Close()
		<-done
	})
	return &local.Client{Dial: listener.Dial, OmitAuth: true}
}

func TestRunningStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []ipn.State
		ready  bool
	}{
		{"persisted", []ipn.State{ipn.Running}, true},
		{"enrollment", []ipn.State{ipn.NoState, ipn.NeedsLogin, ipn.NeedsMachineAuth, ipn.Starting, ipn.Running}, true},
		{"not ready", []ipn.State{ipn.Starting, ipn.NeedsMachineAuth}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := statusClient(t, func(w http.ResponseWriter, r *http.Request) {
				encoder := json.NewEncoder(w)
				// Neither backend diagnostic text nor internal log wording proves readiness.
				message := "AuthLoop: state is Running; done credential-secret"
				encoder.Encode(ipn.Notify{ErrMessage: &message})
				for _, state := range tc.states {
					encoder.Encode(ipn.Notify{State: &state})
				}
			})
			var logs bytes.Buffer
			reportRunning(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), client)
			got := logs.String()
			if strings.Contains(got, `"msg":"tailnet ready","state":"Running"`) != tc.ready {
				t.Fatalf("readiness=%v: %s", tc.ready, got)
			}
			if strings.Count(got, `"msg":"tailnet ready"`) > 1 || strings.Contains(got, "credential-secret") {
				t.Fatalf("duplicate event or backend diagnostic leaked: %s", got)
			}
			if !tc.ready && !strings.Contains(got, "tailnet status unavailable") {
				t.Fatalf("missing watch failure diagnostic: %s", got)
			}
		})
	}
}

func TestRunningStatusCancellation(t *testing.T) {
	connected := make(chan struct{})
	client := statusClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(connected)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		reportRunning(ctx, slog.New(slog.NewJSONHandler(&logs, nil)), client)
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("status watcher did not connect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled status watcher did not stop")
	}
	if logs.Len() != 0 {
		t.Fatalf("canceled watcher logged readiness or an error: %s", &logs)
	}
}

func TestStartupLoggingLevels(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			var logs bytes.Buffer
			app, startup := newLoggers(&logs, level)
			app.Info("application info")
			app.Error("application error")
			url := "https://login.tailscale.com/a/fixture-login"
			startup.Info("tsnet", "message", "To start this tsnet server, go to: "+url)
			client := statusClient(t, func(w http.ResponseWriter, r *http.Request) {
				state := ipn.Running
				json.NewEncoder(w).Encode(ipn.Notify{State: &state})
			})
			reportRunning(context.Background(), startup, client)
			got := logs.String()
			for _, required := range []string{url, `"msg":"tailnet ready","state":"Running"`, "application error"} {
				if !strings.Contains(got, required) {
					t.Fatalf("missing %q at level %s: %s", required, level, got)
				}
			}
			if strings.Contains(got, "application info") != (level <= slog.LevelInfo) {
				t.Fatalf("application level was not respected: %s", got)
			}
		})
	}
}
