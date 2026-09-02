// Command gen-vectors writes testdata/{loconet,z21,withrottle}/*.json.
// Run from the go/ module: go run ./cmd/gen-vectors
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dcc-bigfred/proto/go/drive"
	"github.com/dcc-bigfred/proto/go/loconet"
	"github.com/dcc-bigfred/proto/go/vectors"
	"github.com/dcc-bigfred/proto/go/withrottle"
	"github.com/dcc-bigfred/proto/go/z21"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-vectors: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	td := filepath.Join(root, "testdata")
	writes := []struct {
		path string
		file vectors.File
	}{
		{filepath.Join(td, "loconet", "gpon.json"), loconetCases()},
		{filepath.Join(td, "z21", "frames.json"), z21Cases()},
		{filepath.Join(td, "withrottle", "lines.json"), withrottleCases()},
	}
	for _, w := range writes {
		if err := write(w.path, w.file); err != nil {
			return err
		}
	}
	return nil
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..")), nil
}

func write(path string, f vectors.File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func hx(b []byte) string { return hex.EncodeToString(b) }

func fields(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func loconetCases() vectors.File {
	return vectors.File{Cases: []vectors.Case{
		{
			ID:     "gpon",
			Hex:    hx(loconet.AppendChecksum([]byte{0x83})),
			Op:     "opc_gpon",
			Fields: fields(map[string]any{"opcode": 131}),
		},
		{
			ID:     "gpoff",
			Hex:    hx(loconet.AppendChecksum([]byte{0x82})),
			Op:     "opc_gpoff",
			Fields: fields(map[string]any{"opcode": 130}),
		},
	}}
}

func z21Cases() vectors.File {
	info := z21.BuildLocoInfo(drive.LocoState{
		Addr: 3, Speed: 50, Forward: true, Steps: 128, Functions: 1,
	})
	return vectors.File{Cases: []vectors.Case{
		{ID: "get_serial", Hex: hx(z21.BuildGetSerialNumber()), Op: "get_serial"},
		{
			ID:     "set_drive_3_50_fwd_128",
			Hex:    hx(z21.BuildSetLocoDrive(3, 50, true, 3)),
			Op:     "set_drive",
			Fields: fields(map[string]any{"addr": 3, "speed": 50, "forward": true, "steps": 128}),
		},
		{
			ID:     "set_drive_stop_fwd",
			Hex:    hx(z21.BuildSetLocoDrive(3, 0, true, 3)),
			Op:     "set_drive",
			Fields: fields(map[string]any{"addr": 3, "speed": 0, "forward": true, "steps": 128}),
		},
		{
			ID:     "set_fn_f0_on",
			Hex:    hx(z21.BuildSetLocoFunction(3, 0, true)),
			Op:     "set_function",
			Fields: fields(map[string]any{"addr": 3, "fn": 0, "on": true}),
		},
		{
			ID:     "loco_info_3_50_fwd_f0",
			Hex:    hx(info),
			Op:     "loco_info",
			Fields: fields(map[string]any{"addr": 3, "speed": 50, "forward": true, "steps": 128, "functions": 1}),
		},
		{
			ID:  "broadcast_flags",
			Hex: hx(z21.BuildSetBroadcastFlags(z21.BcDrivingSwitching | z21.BcAllLocos)),
			Op:  "set_broadcast_flags",
		},
	}}
}

func withrottleCases() vectors.File {
	line := func(s string) string { return hx([]byte(s)) }
	cases := []vectors.Case{
		{ID: "hu", Hex: line("HUproto"), Op: "hu"},
		{ID: "acquire_s3", Hex: line("M0+S3<;>S3"), Op: "acquire", Fields: fields(map[string]any{"addr": 3})},
		{ID: "set_speed_50", Hex: line("M0AS3<;>V50"), Op: "set_speed", Fields: fields(map[string]any{"addr": 3, "speed": 50})},
		{ID: "set_dir_fwd", Hex: line("M0AS3<;>R1"), Op: "set_direction", Fields: fields(map[string]any{"addr": 3, "forward": true})},
		{ID: "set_fn_f0_on", Hex: line("M0AS3<;>f10"), Op: "set_function", Fields: fields(map[string]any{"addr": 3, "fn": 0, "on": true})},
		{ID: "track_power_on", Hex: line("PPA1"), Op: "track_power", Fields: fields(map[string]any{"on": true})},
	}
	for _, c := range cases {
		if c.Op == "hu" || c.Op == "track_power" {
			continue
		}
		raw, _ := hex.DecodeString(c.Hex)
		if _, ok := withrottle.ParseM(string(raw)); !ok {
			panic("generated line is not a MultiThrottle command: " + c.ID)
		}
	}
	return vectors.File{Cases: cases}
}