package withrottle

import (
	"encoding/hex"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dcc-bigfred/proto/go/vectors"
)

func testdata(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "testdata", rel)
}

func TestGoldenLines(t *testing.T) {
	f, err := vectors.Load(testdata(t, "withrottle/lines.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range f.Cases {
		raw, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		line := string(raw)
		switch c.Op {
		case "acquire", "set_speed", "set_direction", "set_function":
			if _, ok := ParseM(line); !ok {
				t.Fatalf("%s: ParseM %q", c.ID, line)
			}
		case "hu":
			if line != "HUproto" {
				t.Fatalf("hu = %q", line)
			}
		case "track_power":
			if line != "PPA1" {
				t.Fatalf("ppa = %q", line)
			}
		}
	}
}
