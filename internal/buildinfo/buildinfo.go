package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Commit is injected at build time. The debug fallback keeps local builds useful.
var Commit = "unknown"

func Revision() string {
	if value := normalize(Commit); value != "" && value != "unknown" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				if value := normalize(setting.Value); value != "" {
					return value
				}
			}
		}
	}
	return "unknown"
}

func ShortRevision() string {
	revision := Revision()
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}

func normalize(value string) string {
	return strings.TrimSpace(strings.Trim(value, "\""))
}
