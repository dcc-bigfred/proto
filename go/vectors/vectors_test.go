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
	// go/vectors → repo root testdata/
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
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
}
