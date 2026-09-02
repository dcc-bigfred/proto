package commandstation

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/drive"
	"github.com/dcc-bigfred/proto/go/withrottle"
)

type wtHost struct {
	mu     sync.Mutex
	speeds []drive.LocoState
	locos  map[uint16]drive.LocoState
}

func (h *wtHost) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, steps uint8) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locos == nil {
		h.locos = map[uint16]drive.LocoState{}
	}
	st := h.locos[addr]
	st.Addr, st.Speed, st.Forward, st.Steps = addr, speed, forward, steps
	h.locos[addr] = st
	h.speeds = append(h.speeds, st)
	return nil
}
func (h *wtHost) SetFunction(drive.ClientID, uint16, uint8, bool) error { return nil }
func (h *wtHost) LocoState(addr uint16) (drive.LocoState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st, ok := h.locos[addr]; ok {
		if st.Steps == 0 {
			st.Steps = 128
		}
		return st, nil
	}
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
}
func (h *wtHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (h *wtHost) Release(drive.ClientID, uint16)           {}

func TestNewWiThrottleLoopback(t *testing.T) {
	host := &wtHost{}
	srv, err := withrottle.Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	tcp := srv.Addr().(*net.TCPAddr)
	st, err := NewWiThrottle("127.0.0.1", uint16(tcp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.CleanUp() })

	if err := st.SetSpeed(3, 50, true, 128); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		ok := len(host.speeds) > 0 && host.speeds[len(host.speeds)-1].Speed == 50
		host.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	host.mu.Lock()
	speeds := append([]drive.LocoState(nil), host.speeds...)
	host.mu.Unlock()
	if len(speeds) == 0 || speeds[len(speeds)-1].Addr != 3 {
		t.Fatalf("speeds = %+v", speeds)
	}

	if err := st.SendFn(MainTrackMode, 3, 0, false); err != nil {
		t.Fatal(err)
	}

	if err := st.EmergencyStop(3, true); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		ok := len(host.speeds) > 0 && host.speeds[len(host.speeds)-1].Speed == 1
		host.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	host.mu.Lock()
	last := host.speeds[len(host.speeds)-1]
	host.mu.Unlock()
	if last.Addr != 3 || last.Speed != 1 || !last.Forward {
		t.Fatalf("EmergencyStop speed = %+v", last)
	}

	if _, err := st.ReadCV(MainTrackMode, LocoCV{}); err != ErrUnsupported {
		t.Fatalf("ReadCV = %v", err)
	}
	if err := st.WriteCV(MainTrackMode, LocoCV{}); err != ErrUnsupported {
		t.Fatalf("WriteCV = %v", err)
	}
	if snap := st.Metrics(); snap.LinesTx == 0 {
		t.Fatal("expected client traffic in Metrics")
	}
}
