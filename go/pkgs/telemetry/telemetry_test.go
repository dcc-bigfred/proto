package telemetry

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/dcc-bigfred/proto/go/pkgs/commandstation"
	"github.com/dcc-bigfred/proto/go/pkgs/withrottle"
)

type lnSrc struct {
	s commandstation.LnMetricsSnapshot
}

func (s lnSrc) MetricsSnapshot() commandstation.LnMetricsSnapshot { return s.s }

type z21Src struct {
	s commandstation.Z21MetricsSnapshot
}

func (s z21Src) Z21MetricsSnapshot() commandstation.Z21MetricsSnapshot { return s.s }

type wtSrc struct{ s withrottle.Snapshot }

func (s wtSrc) Metrics() withrottle.Snapshot { return s.s }

func TestRegisterNil(t *testing.T) {
	reg, err := RegisterLocoNet(nil, Config{})
	if err != nil || reg != nil {
		t.Fatalf("RegisterLocoNet(nil) = %v, %v", reg, err)
	}
	reg, err = RegisterZ21(nil, Config{})
	if err != nil || reg != nil {
		t.Fatalf("RegisterZ21(nil) = %v, %v", reg, err)
	}
	reg, err = RegisterWithrottle(nil, Config{})
	if err != nil || reg != nil {
		t.Fatalf("RegisterWithrottle(nil) = %v, %v", reg, err)
	}
}

func TestRegisterNoopMeter(t *testing.T) {
	cfg := Config{Attrs: []attribute.KeyValue{attribute.Int("layout.id", 1)}}
	ln, err := RegisterLocoNet(lnSrc{s: commandstation.LnMetricsSnapshot{
		TxFrames:   3,
		TxByOpcode: map[byte]uint64{0xA0: 1},
	}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if ln == nil {
		t.Fatal("expected registration")
	}
	t.Cleanup(func() { _ = ln.Unregister() })

	z, err := RegisterZ21(z21Src{s: commandstation.Z21MetricsSnapshot{
		TxPackets: 2,
		TxByType:  map[byte]uint64{0xE4: 1},
	}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if z == nil {
		t.Fatal("expected registration")
	}
	t.Cleanup(func() { _ = z.Unregister() })

	wt, err := RegisterWithrottle(wtSrc{s: withrottle.Snapshot{LinesTx: 4}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if wt == nil {
		t.Fatal("expected registration")
	}
	t.Cleanup(func() { _ = wt.Unregister() })
}
