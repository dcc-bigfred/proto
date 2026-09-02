package withrottle

import (
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
		raw, err := c.Bytes()
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

func TestGoldenFunctionPress(t *testing.T) {
	f, err := vectors.Load(testdata(t, "withrottle/function_press.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		raw, err := c.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		line := string(raw)
		cmd, ok := ParseM(line)
		if !ok || len(cmd.Properties) == 0 {
			t.Fatalf("%s: ParseM %q", c.ID, line)
		}
		prop := cmd.Properties[0]
		switch c.Op {
		case "press", "release":
			if _, _, ok := parsePress(prop); !ok {
				t.Fatalf("%s: parsePress %q", c.ID, prop)
			}
		case "force":
			if _, _, ok := parseForce(prop); !ok {
				t.Fatalf("%s: parseForce %q", c.ID, prop)
			}
		case "mode":
			if _, _, ok := parseMode(prop); !ok {
				t.Fatalf("%s: parseMode %q", c.ID, prop)
			}
		}
	}
}
