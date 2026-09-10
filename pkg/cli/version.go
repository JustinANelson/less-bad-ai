package cli

// These values are replaced by release builds through -ldflags. Keeping useful
// development defaults makes locally built binaries self-describing.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)
