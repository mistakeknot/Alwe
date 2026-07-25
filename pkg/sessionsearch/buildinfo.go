package sessionsearch

import (
	"runtime/debug"
)

// buildInfo returns the running binary's VCS revision (or module version) and
// module path.
//
// This exists because of a concrete failure: a patched alwe binary was
// installed while MCP servers started minutes earlier kept serving the old
// code, and nothing in the health output distinguished them. Reporting the
// build makes a stale server detectable instead of invisible.
func buildInfo() (id, module string) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	module = bi.Main.Path

	var revision, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}

	switch {
	case revision != "":
		id = revision
		if len(id) > 12 {
			id = id[:12]
		}
		if modified == "true" {
			id += "+dirty"
		}
	case bi.Main.Version != "":
		id = bi.Main.Version
	}
	return id, module
}
