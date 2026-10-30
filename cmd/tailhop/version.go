package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Release builds set these from the validated tag and commit using -ldflags -X.
var version = "dev"
var commit string

func versionString() string {
	revision := commit
	if revision == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			dirty := false
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					revision = setting.Value
				case "vcs.modified":
					dirty = setting.Value == "true"
				}
			}
			if revision != "" && dirty {
				revision += "-dirty"
			}
		}
	}
	if revision == "" {
		revision = "unknown"
	}
	return fmt.Sprintf("tailhop %s (commit %s; %s)", version, revision, runtime.Version())
}
