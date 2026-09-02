package commandstation

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func collectScan(ctx context.Context, a Autodetection) ([]DetectedConnection, error) {
	var (
		mu  sync.Mutex
		out []DetectedConnection
	)
	err := a.Scan(ctx, func(c DetectedConnection) error {
		mu.Lock()
		out = append(out, c)
		mu.Unlock()
		return nil
	})
	return out, err
}

func TestLocoNetSerialAutodetection(t *testing.T) {
	origList := listSerialPorts
	origExists := serialDeviceExists
	t.Cleanup(func() {
		listSerialPorts = origList
		serialDeviceExists = origExists
	})

	listSerialPorts = func() ([]string, error) {
		return []string{"/dev/ttyUSB0", "/dev/ttyACM0"}, nil
	}
	serialDeviceExists = func(string) bool { return false }

	got, err := collectScan(context.Background(), LocoNetSerialAutodetection{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// 4 autodetect + 2 devices × 4 baud
	if want := 4 + 2*4; len(got) != want {
		t.Fatalf("len=%d want %d: %+v", len(got), want, got)
	}
	if got[0].URI != "serial://autodetect:19200" {
		t.Fatalf("first autodetect URI: %q", got[0].URI)
	}
	foundUSB := false
	for _, c := range got {
		if c.URI == "serial:///dev/ttyUSB0:57600" {
			foundUSB = true
			if !strings.Contains(c.Name, "/dev/ttyUSB0") {
				t.Fatalf("name %q missing device", c.Name)
			}
		}
	}
	if !foundUSB {
		t.Fatal("missing serial:///dev/ttyUSB0:57600")
	}
}

func TestLocoNetTCPAutodetection(t *testing.T) {
	open := map[string]bool{
		"192.168.0.10:1234": true,
		"192.168.0.20:5550": true,
	}
	dial := func(_ context.Context, address string) (net.Conn, error) {
		if !open[address] {
			return nil, errors.New("refused")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}

	got, err := collectScan(context.Background(), LocoNetTCPAutodetection{
		SubnetPrefix: "192.168.0",
		Dial:         dial,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results: %+v", len(got), got)
	}
	uris := map[string]bool{}
	for _, c := range got {
		uris[c.URI] = true
	}
	if !uris["tcp://192.168.0.10:1234"] || !uris["lbserver://192.168.0.20:5550"] {
		t.Fatalf("unexpected URIs: %+v", got)
	}
}

func TestZ21AutodetectionPreferredHost(t *testing.T) {
	probed := make([]string, 0)
	probe := func(_ context.Context, address string, _ []byte) error {
		probed = append(probed, address)
		if address == "10.0.0.111:21105" {
			return nil
		}
		return errors.New("no reply")
	}
	got, err := collectScan(context.Background(), Z21Autodetection{
		SubnetPrefix: "10.0.0",
		Probe:        probe,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].URI != "udp://10.0.0.111:21105" {
		t.Fatalf("got %+v", got)
	}
	if len(probed) != 1 {
		t.Fatalf("expected early exit after .111, probed %v", probed)
	}
}

func TestZ21AutodetectionFullScan(t *testing.T) {
	probe := func(_ context.Context, address string, _ []byte) error {
		if address == "10.0.0.42:21105" {
			return nil
		}
		return errors.New("no reply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := collectScan(ctx, Z21Autodetection{
		SubnetPrefix: "10.0.0",
		Probe:        probe,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].URI != "udp://10.0.0.42:21105" {
		t.Fatalf("got %+v", got)
	}
}

func TestMultiAutodetectionParallel(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	a := MultiAutodetection{
		stubAutodetectParallel{name: "a", uri: "udp://1", started: started, release: release},
		stubAutodetectParallel{name: "b", uri: "tcp://2", started: started, release: release},
	}

	done := make(chan []DetectedConnection, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := collectScan(context.Background(), a)
		if err != nil {
			errCh <- err
			return
		}
		done <- got
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("scanners did not start in parallel")
		}
	}
	close(release)

	select {
	case got := <-done:
		if len(got) != 2 {
			t.Fatalf("got %d", len(got))
		}
		names := map[string]bool{}
		for _, c := range got {
			names[c.Name] = true
		}
		if !names["a"] || !names["b"] {
			t.Fatalf("missing names: %+v", got)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("Scan timed out")
	}
}

func TestMultiAutodetectionSoftFail(t *testing.T) {
	a := MultiAutodetection{
		stubAutodetectFail{err: errors.New("boom")},
		stubAutodetect{DetectedConnection{Name: "ok", URI: "udp://1"}},
	}
	got, err := collectScan(context.Background(), a)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("partial results: %+v", got)
	}
}

func TestDefaultUDPProberRespectsPerProbeTimeout(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	probe := defaultUDPProber(200 * time.Millisecond)
	start := time.Now()
	err = probe(ctx, pc.LocalAddr().String(), []byte{0x04, 0x00, 0x10, 0x00})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout/read error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("probe took %v; expected ~200ms despite 30s ctx deadline", elapsed)
	}
}

func TestScanTCPHostsDeadlineNotError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dial := func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("refused")
	}
	err := scanTCPHosts(ctx, "10.255.255", []int{9}, dial, func(string, int) {})
	if err != nil {
		t.Fatalf("DeadlineExceeded must not be a scanner error: %v", err)
	}
}

type stubAutodetectParallel struct {
	name, uri string
	started   chan<- struct{}
	release   <-chan struct{}
}

func (s stubAutodetectParallel) Scan(ctx context.Context, emit EmitFunc) error {
	select {
	case s.started <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return emit(DetectedConnection{Name: s.name, URI: s.uri})
}

type stubAutodetect []DetectedConnection

func (s stubAutodetect) Scan(_ context.Context, emit EmitFunc) error {
	for _, c := range s {
		if err := emit(c); err != nil {
			return err
		}
	}
	return nil
}

type stubAutodetectFail struct{ err error }

func (s stubAutodetectFail) Scan(context.Context, EmitFunc) error {
	return s.err
}

func TestResolveSerialDeviceUsesCandidates(t *testing.T) {
	origList := listSerialPorts
	origExists := serialDeviceExists
	t.Cleanup(func() {
		listSerialPorts = origList
		serialDeviceExists = origExists
	})
	listSerialPorts = func() ([]string, error) {
		return []string{"/dev/ttyS0", "/dev/ttyUSB1"}, nil
	}
	serialDeviceExists = func(string) bool { return false }
	got, err := resolveSerialDevice()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/ttyUSB1" {
		t.Fatalf("got %q", got)
	}
}
