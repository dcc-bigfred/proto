// Package vectors is the shared JSON schema for testdata/{protocol}/*.json.
package vectors

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// File is one golden-vector document.
type File struct {
	Cases []Case `json:"cases"`
}

// Case is one encode/decode example.
// Binary protocols (Z21, LocoNet) use Hex; text protocols (WiThrottle) use Line.
// Exactly one of Hex or Line must be set.
type Case struct {
	ID     string          `json:"id"`
	Hex    string          `json:"hex,omitempty"`
	Line   string          `json:"line,omitempty"`
	Op     string          `json:"op"`
	Fields json.RawMessage `json:"fields,omitempty"`
}

// Bytes returns the on-wire payload: Line as UTF-8, or decoded Hex.
func (c Case) Bytes() ([]byte, error) {
	hasLine := c.Line != ""
	hasHex := c.Hex != ""
	switch {
	case hasLine && hasHex:
		return nil, fmt.Errorf("vectors: case %q has both line and hex", c.ID)
	case hasLine:
		return []byte(c.Line), nil
	case hasHex:
		b, err := hex.DecodeString(c.Hex)
		if err != nil {
			return nil, fmt.Errorf("vectors: case %q hex: %w", c.ID, err)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("vectors: case %q missing line and hex", c.ID)
	}
}

// Load reads a testdata JSON file.
func Load(path string) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, err
	}
	return f, nil
}
