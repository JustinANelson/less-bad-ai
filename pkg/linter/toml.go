package linter

import "github.com/pelletier/go-toml/v2"

func decodeTOML(b []byte, v any) error { return toml.Unmarshal(b, v) }
