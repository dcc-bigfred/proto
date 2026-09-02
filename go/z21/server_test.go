package z21

import (
	"net"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/drive"
)

type recHost struct {
	speeds []drive.LocoState
	fns    []struct {
		addr uint16
		fn   uint8
		on   bool
	}
	locos map[uint16]drive.LocoState
}

func (h *recHost) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, steps uint8) error {
	st := drive.LocoState{Addr: addr, Speed: speed, Forward: forward, Steps: steps}
	if h.locos == nil {
		h.locos = map[uint16]drive.LocoState{}
	}
	cur := h.locos[addr]
	cur.Addr, cur.Speed, cur.Forward, cur.Steps = addr, speed, forward, steps
	h.locos[addr] = cur
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
func (h *recHost) SetTrackPower(drive.ClientID, bool) error { return nil }
func (h *recHost) Release(drive.ClientID, uint16)           {}

func TestListenDriveAndFunction(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.DialUDP("udp", nil, srv.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := conn.Write(BuildGetSerialNumber()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	serial, ok := ParseSerialReply(buf[:n])
	if !ok || serial != 258_000_001 {
		t.Fatalf("serial reply ok=%v serial=%d pkt=% X", ok, serial, buf[:n])
	}

	if _, err := conn.Write(BuildSetLocoDrive(3, 50, true, 3)); err != nil {
		t.Fatal(err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	info, ok := ParseLocoInfo(buf[:n])
	if !ok || info.Addr != 3 || info.Speed != 50 || !info.Forward {
		t.Fatalf("loco info after drive: %+v ok=%v pkt=% X", info, ok, buf[:n])
	}
	if len(host.speeds) != 1 || host.speeds[0].Addr != 3 || host.speeds[0].Speed != 50 {
		t.Fatalf("host speeds = %+v", host.speeds)
	}

	if _, err := conn.Write(BuildSetLocoFunction(3, 0, true)); err != nil {
		t.Fatal(err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	info, ok = ParseLocoInfo(buf[:n])
	if !ok || info.Functions&1 == 0 {
		t.Fatalf("expected F0 on, info=%+v pkt=% X", info, buf[:n])
	}
	if len(host.fns) != 1 || host.fns[0].fn != 0 || !host.fns[0].on {
		t.Fatalf("host fns = %+v", host.fns)
	}
}

func TestEncodeDriveDB3StopKeepsR(t *testing.T) {
	if got := EncodeDriveDB3(0, true, 3); got != 0x80 {
		t.Fatalf("got %#02x", got)
	}
}

func TestSplitDatagram(t *testing.T) {
	a := BuildGetLocoInfo(4)
	b := BuildGetLocoInfo(7)
	pkts := SplitDatagram(append(append([]byte{}, a...), b...))
	if len(pkts) != 2 {
		t.Fatalf("len=%d", len(pkts))
	}
}
