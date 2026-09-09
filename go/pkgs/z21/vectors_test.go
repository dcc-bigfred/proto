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

func TestGoldenCV(t *testing.T) {
	f, err := vectors.Load(testdata(t, "z21/cv.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		want, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		var got []byte
		switch c.ID {
		case "cv_read_1":
			got = BuildProgRead(0)
		case "cv_write_8_0x20":
			got = BuildProgWrite(7, 0x20)
		case "pom_read_128_cv1":
			got = BuildPomRead(128, 0)
		case "cv_result_8_0x20":
			got = BuildCvResult(8, 0x20)
		case "cv_nack":
			got = BuildCvNack()
		case "cv_nack_sc":
			got = BuildCvNackSC()
		default:
			t.Fatalf("unknown case %s", c.ID)
		}
		if hex.EncodeToString(got) != c.Hex {
			t.Fatalf("%s:\n got % X\nwant % X", c.ID, got, want)
		}
		switch c.Op {
		case "cv_result":
			r, ok := ParseCvReply(got)
			if !ok || r.Kind != CvResult || r.CV != 8 || r.Value != 0x20 {
				t.Fatalf("parse result: %+v ok=%v", r, ok)
			}
		case "cv_nack":
			r, ok := ParseCvReply(got)
			if !ok || r.Kind != CvNack {
				t.Fatalf("parse nack: %+v ok=%v", r, ok)
			}
		case "cv_nack_sc":
			r, ok := ParseCvReply(got)
			if !ok || r.Kind != CvNackSC {
				t.Fatalf("parse nack_sc: %+v ok=%v", r, ok)
			}
		}
	}
}

func TestAddressFromCVs(t *testing.T) {
	addr, long, ok := AddressFromCVs(7, 0, 0, 0x06)
	if !ok || addr != 7 || long {
		t.Fatalf("short: addr=%d long=%v ok=%v", addr, long, ok)
	}
	addr, long, ok = AddressFromCVs(0, 0xC4, 0xD2, 0x26)
	if !ok || addr != 1234 || !long {
		t.Fatalf("long: addr=%d long=%v ok=%v", addr, long, ok)
	}
	addr, long, ok = AddressFromCVs(0xC8, 0, 0, 0x06)
	if !ok || addr != 72 || long {
		t.Fatalf("short mask: addr=%d long=%v ok=%v", addr, long, ok)
	}
}

func TestAddressCVWrites(t *testing.T) {
	writes, long, err := AddressCVWrites(7, 0x26)
	if err != nil || long || len(writes) != 2 {
		t.Fatalf("short: %+v long=%v err=%v", writes, long, err)
	}
	if writes[0] != (CVWrite{1, 7}) || writes[1] != (CVWrite{29, 0x06}) {
		t.Fatalf("short writes: %+v", writes)
	}
	writes, long, err = AddressCVWrites(1234, 0x06)
	if err != nil || !long || len(writes) != 3 {
		t.Fatalf("long: %+v long=%v err=%v", writes, long, err)
	}
	if writes[0].CV != 17 || writes[0].Value != 0xC4 || writes[1].Value != 0xD2 || writes[2].Value != 0x26 {
		t.Fatalf("long writes: %+v", writes)
	}
	if _, _, err := AddressCVWrites(0, 0); err != ErrInvalidAddress {
		t.Fatalf("zero addr: %v", err)
	}
	if _, _, err := AddressCVWrites(LongMax+1, 0); err != ErrInvalidAddress {
		t.Fatalf("above LongMax: %v", err)
	}
	writes, long, err = AddressCVWrites(LongMax, 0x06)
	if err != nil || !long || len(writes) != 3 {
		t.Fatalf("LongMax: %+v long=%v err=%v", writes, long, err)
	}
}

func TestAddressCVWritesRailComPlus(t *testing.T) {
	cur := byte(131)
	writes, _, err := AddressCVWrites(121, 30, WithRailComPlusDisabled(true, &cur))
	if err != nil {
		t.Fatal(err)
	}
	if writes[0] != (CVWrite{RailComPlusCV, 3}) {
		t.Fatalf("prepend: %+v", writes)
	}
	if writes[1].CV != 1 || writes[2].CV != 29 {
		t.Fatalf("address writes: %+v", writes)
	}

	alreadyOff := byte(3)
	writes, _, err = AddressCVWrites(121, 30, WithRailComPlusDisabled(true, &alreadyOff))
	if err != nil || writes[0].CV == RailComPlusCV {
		t.Fatalf("already off should skip CV28: %+v err=%v", writes, err)
	}

	writes, _, err = AddressCVWrites(121, 30, WithRailComPlusDisabled(true, nil))
	if err != nil || writes[0].CV == RailComPlusCV {
		t.Fatalf("unread CV28 should skip: %+v err=%v", writes, err)
	}

	off := byte(3)
	writes, _, err = AddressCVWrites(7, 6, WithRailComPlusDisabled(false, &off))
	if err != nil || writes[0] != (CVWrite{RailComPlusCV, 131}) {
		t.Fatalf("enable: %+v err=%v", writes, err)
	}
}

func TestParseCvReplyConcatenated(t *testing.T) {
	serial := BuildSerialReply(1)
	result := BuildCvResult(8, 0x20)
	buf := append(append([]byte{}, serial...), result...)
	r, ok := ParseCvReply(buf)
	if !ok || r.Kind != CvResult || r.CV != 8 || r.Value != 0x20 {
		t.Fatalf("concatenated: %+v ok=%v", r, ok)
	}
}
