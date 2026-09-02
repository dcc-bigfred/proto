// Command gen-vectors writes testdata/{loconet,z21,withrottle}/*.json.
// Run from the go/ module: go run ./cmd/gen-vectors
package main

import (
	"bytes"
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
		{filepath.Join(td, "z21", "function_group.json"), z21FunctionGroupCases()},
		{filepath.Join(td, "withrottle", "lines.json"), withrottleCases()},
		{filepath.Join(td, "withrottle", "function_press.json"), withrottlePressCases()},
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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
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
	cases := []vectors.Case{
		{ID: "hu", Line: "HUproto", Op: "hu"},
		{ID: "acquire_s3", Line: "M0+S3<;>S3", Op: "acquire", Fields: fields(map[string]any{"addr": 3})},
		{ID: "set_speed_50", Line: "M0AS3<;>V50", Op: "set_speed", Fields: fields(map[string]any{"addr": 3, "speed": 50})},
		{ID: "set_dir_fwd", Line: "M0AS3<;>R1", Op: "set_direction", Fields: fields(map[string]any{"addr": 3, "forward": true})},
		{ID: "set_fn_f0_on", Line: "M0AS3<;>f10", Op: "set_function", Fields: fields(map[string]any{"addr": 3, "fn": 0, "on": true})},
		{ID: "track_power_on", Line: "PPA1", Op: "track_power", Fields: fields(map[string]any{"on": true})},
		{ID: "estop_s3", Line: "M0AS3<;>X", Op: "estop", Fields: fields(map[string]any{"addr": 3})},
	}
	for _, c := range cases {
		if c.Op == "hu" || c.Op == "track_power" {
			continue
		}
		if _, ok := withrottle.ParseM(c.Line); !ok {
			panic("generated line is not a MultiThrottle command: " + c.ID)
		}
	}
	return vectors.File{Cases: cases}
}

func z21FunctionGroupCases() vectors.File {
	return vectors.File{Cases: []vectors.Case{
		{
			ID:     "group1_f0_f1",
			Hex:    hx(z21.BuildSetLocoFunctionGroup(3, 0x20, 0x03)),
			Op:     "set_function_group",
			Fields: fields(map[string]any{"addr": 3, "db0": 0x20, "lo": 0, "hi": 4, "bits": 3}),
		},
		{
			ID:     "group2_f5",
			Hex:    hx(z21.BuildSetLocoFunctionGroup(3, 0x21, 0x01)),
			Op:     "set_function_group",
			Fields: fields(map[string]any{"addr": 3, "db0": 0x21, "lo": 5, "hi": 8, "bits": 1}),
		},
		{
			ID:     "group3_f9_f12",
			Hex:    hx(z21.BuildSetLocoFunctionGroup(3, 0x22, 0x0F)),
			Op:     "set_function_group",
			Fields: fields(map[string]any{"addr": 3, "db0": 0x22, "lo": 9, "hi": 12, "bits": 15}),
		},
		{
			ID:     "group4_f13",
			Hex:    hx(z21.BuildSetLocoFunctionGroup(3, 0x23, 0x01)),
			Op:     "set_function_group",
			Fields: fields(map[string]any{"addr": 3, "db0": 0x23, "lo": 13, "hi": 20, "bits": 1}),
		},
		{
			ID:     "group5_f21",
			Hex:    hx(z21.BuildSetLocoFunctionGroup(3, 0x28, 0x01)),
			Op:     "set_function_group",
			Fields: fields(map[string]any{"addr": 3, "db0": 0x28, "lo": 21, "hi": 28, "bits": 1}),
		},
		{
			ID:     "group6_f29",
			Hex:    hx(z21.BuildSetLocoFunctionGroup(3, 0x29, 0x01)),
			Op:     "set_function_group",
			Fields: fields(map[string]any{"addr": 3, "db0": 0x29, "lo": 29, "hi": 31, "bits": 1}),
		},
	}}
}

func withrottlePressCases() vectors.File {
	return vectors.File{Cases: []vectors.Case{
		{ID: "press_f1", Line: "M0AS3<;>F11", Op: "press", Fields: fields(map[string]any{"addr": 3, "fn": 1, "pressed": true})},
		{ID: "release_f1", Line: "M0AS3<;>F01", Op: "release", Fields: fields(map[string]any{"addr": 3, "fn": 1, "pressed": false})},
		{ID: "force_f0_on", Line: "M0AS3<;>f10", Op: "force", Fields: fields(map[string]any{"addr": 3, "fn": 0, "on": true})},
		{ID: "mode_f2_momentary", Line: "M0AS3<;>m12", Op: "mode", Fields: fields(map[string]any{"addr": 3, "fn": 2, "momentary": true})},
	}}
}
