// Package vectors is the shared JSON schema for testdata/{protocol}/*.json.
package vectors

import (
	"encoding/json"
	"os"
)

// File is one golden-vector document.
type File struct {
	Cases []Case `json:"cases"`
}

// Case is one encode/decode example.
type Case struct {
	ID     string          `json:"id"`
	Hex    string          `json:"hex"`
	Op     string          `json:"op"`
	Fields json.RawMessage `json:"fields,omitempty"`
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
