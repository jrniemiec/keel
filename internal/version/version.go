// Package version gives the version keel and kstat report.
package version

import "runtime/debug"

// String returns set, the version given at build time with -X (the Makefile
// and the release build do); if it is "dev", the module version go install
// records, e.g. "v0.8.0"; else "dev".
func String(set string) string {
	if set != "dev" {
		return set
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return set
}
