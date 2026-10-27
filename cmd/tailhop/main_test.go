package main

import (
	"io"
	"os"
	"strings"
	"testing"
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
