package z21

import (
	"encoding/hex"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dcc-bigfred/proto/go/internal/vectors"
	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

func testdata(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "testdata", rel)
}

func TestGoldenFrames(t *testing.T) {
	f, err := vectors.Load(testdata(t, "z21/frames.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		want, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatalf("%s: hex: %v", c.ID, err)
		}
		var got []byte
		switch c.ID {
		case "get_serial":
			got = BuildGetSerialNumber()
		case "set_drive_3_50_fwd_128":
			got = BuildSetLocoDrive(3, 50, true, 3)
		case "set_drive_stop_fwd":
			got = BuildSetLocoDrive(3, 0, true, 3)
		case "set_fn_f0_on":
			got = BuildSetLocoFunction(3, 0, true)
		case "loco_info_3_50_fwd_f0":
			got = BuildLocoInfo(drive.LocoState{Addr: 3, Speed: 50, Forward: true, Steps: 128, Functions: 1})
		case "broadcast_flags":
			got = BuildSetBroadcastFlags(BcDrivingSwitching | BcAllLocos)
		default:
			t.Fatalf("unknown case %s", c.ID)
		}
		if hex.EncodeToString(got) != c.Hex {
			t.Fatalf("%s:\n got % X\nwant % X", c.ID, got, want)
		}
		if c.Op == "loco_info" {
			info, ok := ParseLocoInfo(got)
			if !ok || info.Addr != 3 || info.Speed != 50 || !info.Forward || info.Functions&1 == 0 {
				t.Fatalf("parse loco_info: %+v ok=%v", info, ok)
			}
		}
		if c.Op == "set_drive" {
			addr, speed, forward, ok := ParseSetLocoDrive(got)
			if !ok {
				t.Fatalf("ParseSetLocoDrive %s", c.ID)
			}
			if addr != 3 {
				t.Fatalf("%s addr=%d", c.ID, addr)
			}
			_ = speed
			_ = forward
		}
	}
}

func TestGoldenFunctionGroup(t *testing.T) {
	f, err := vectors.Load(testdata(t, "z21/function_group.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range f.Cases {
		want, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		addr, lo, hi, bits, ok := ParseSetLocoFunctionGroup(want)
		if !ok {
			t.Fatalf("%s: parse", c.ID)
		}
		_ = addr
		_ = lo
		_ = hi
		_ = bits
		got := BuildSetLocoFunctionGroup(addr, want[5], bits)
		if hex.EncodeToString(got) != c.Hex {
			t.Fatalf("%s round-trip:\n got % X\nwant % X", c.ID, got, want)
		}
	}
}
