package z21_test

import (
	"net"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/commandstation"
	"github.com/dcc-bigfred/proto/go/drive"
	"github.com/dcc-bigfred/proto/go/z21"
)

type recHost struct {
	speeds []drive.LocoState
	fns    []struct {
		addr uint16
		fn   uint8
		on   bool
	}
	power *bool
	locos map[uint16]drive.LocoState
}

func (h *recHost) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, steps uint8) error {
	if h.locos == nil {
		h.locos = map[uint16]drive.LocoState{}
	}
	st := h.locos[addr]
	st.Addr, st.Speed, st.Forward, st.Steps = addr, speed, forward, steps
	h.locos[addr] = st
	h.speeds = append(h.speeds, st)
	return nil
}
func (h *recHost) SetFunction(_ drive.ClientID, addr uint16, fn uint8, on bool) error {
	h.fns = append(h.fns, struct {
		addr uint16
		fn   uint8
		on   bool
	}{addr, fn, on})
	if h.locos == nil {
		h.locos = map[uint16]drive.LocoState{}
	}
	st := h.locos[addr]
	st.Addr = addr
	if on {
		st.Functions |= 1 << fn
	} else {
		st.Functions &^= 1 << fn
	}
	h.locos[addr] = st
	return nil
}
func (h *recHost) LocoState(addr uint16) (drive.LocoState, error) {
	if h.locos != nil {
		if st, ok := h.locos[addr]; ok {
			if st.Steps == 0 {
				st.Steps = 128
			}
			return st, nil
		}
	}
	return drive.LocoState{Addr: addr, Steps: 128}, nil
}
func (h *recHost) SetTrackPower(_ drive.ClientID, on bool) error {
	h.power = &on
	return nil
}
func (h *recHost) Release(drive.ClientID, uint16) {}

func TestNewZ21RocoLoopback(t *testing.T) {
	host := &recHost{}
	srv, err := z21.Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	udp := srv.Addr().(*net.UDPAddr)
	cli, err := commandstation.NewZ21Roco("127.0.0.1", uint16(udp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.CleanUp() })

	if err := cli.SetSpeed(3, 50, true, 128); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(host.speeds) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.speeds) == 0 || host.speeds[0].Speed != 50 {
		t.Fatalf("SetSpeed host=%+v", host.speeds)
	}

	speed, forward, err := cli.GetSpeed(3)
	if err != nil {
		t.Fatal(err)
	}
	if speed != 50 || !forward {
		t.Fatalf("GetSpeed = %d fwd=%v (SET S=3 vs INFO KKK=4)", speed, forward)
	}

	if err := cli.SendFn(commandstation.MainTrackMode, 3, 0, false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(host.fns) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.fns) == 0 || host.fns[0].fn != 0 {
		t.Fatalf("SendFn host=%+v", host.fns)
	}

	if err := cli.SetTrackPower(true); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && host.power == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if host.power == nil || !*host.power {
		t.Fatalf("SetTrackPower = %v", host.power)
	}

	if err := cli.EmergencyStop(3, true); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(host.speeds) > 0 && host.speeds[len(host.speeds)-1].Speed == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.speeds) == 0 || host.speeds[len(host.speeds)-1].Speed != 1 {
		t.Fatalf("EmergencyStop host=%+v", host.speeds)
	}

	obs := cli.ObserveStates()
	time.Sleep(50 * time.Millisecond)
drain:
	for {
		select {
		case <-obs:
		default:
			break drain
		}
	}
	srv.NotifyLocoState(drive.LocoState{Addr: 3, Speed: 30, Forward: true, Steps: 128, Functions: 1})
	select {
	case o := <-obs:
		if o.Addr != 3 || !o.HasSpeed || o.Speed != 30 {
			t.Fatalf("obs = %+v", o)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyLocoState not observed")
	}
}
