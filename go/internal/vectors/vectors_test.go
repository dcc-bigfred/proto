package vectors

import (
	"path/filepath"
	"runtime"
	"testing"
)

func testdata(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// go/internal/vectors → repo root testdata/
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "testdata", rel)
}

func TestLoadGpon(t *testing.T) {
	f, err := Load(testdata(t, "loconet/gpon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gpon *Case
	for i := range f.Cases {
		if f.Cases[i].ID == "gpon" {
			gpon = &f.Cases[i]
			break
		}
	}
	if gpon == nil {
		t.Fatal("missing gpon case")
	}
	if gpon.Hex != "837c" || gpon.Op != "opc_gpon" {
		t.Fatalf("unexpected case: %+v", gpon)
	}
	raw, err := gpon.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 || raw[0] != 0x83 {
		t.Fatalf("Bytes = %x", raw)
	}
}

func TestCaseBytesLine(t *testing.T) {
	c := Case{ID: "hu", Line: "HUproto", Op: "hu"}
	raw, err := c.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "HUproto" {
		t.Fatalf("Bytes = %q", raw)
	}
}

func TestCaseBytesRejectsBoth(t *testing.T) {
	c := Case{ID: "x", Hex: "50", Line: "P"}
	if _, err := c.Bytes(); err == nil {
		t.Fatal("expected error")
	}
}

func TestCaseBytesRejectsNeither(t *testing.T) {
	c := Case{ID: "x"}
	if _, err := c.Bytes(); err == nil {
		t.Fatal("expected error")
	}
}
