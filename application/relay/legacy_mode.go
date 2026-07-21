package relay

import "os"

func legacyModeEnabled() bool {
	return parseBoolEnv(os.Getenv("LEGACY_MODE"), false)
}
