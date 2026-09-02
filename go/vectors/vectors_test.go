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
	if len(f.Cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(f.Cases))
	}
	c := f.Cases[0]
	if c.ID != "gpon" || c.Hex != "837c" || c.Op != "opc_gpon" {
		t.Fatalf("unexpected case: %+v", c)
	}
}
