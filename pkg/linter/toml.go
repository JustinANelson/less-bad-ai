package linter

import (
	"bytes"

	"github.com/pelletier/go-toml/v2"
)

func decodeTOML(b []byte, v any) error {
	return toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(v)
}
