package z21

import (
	"encoding/binary"
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
	released []struct {
		id   drive.ClientID
		addr uint16
	}
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
func (h *recHost) Release(id drive.ClientID, addr uint16) {
	h.released = append(h.released, struct {
		id   drive.ClientID
		addr uint16
	}{id, addr})
}

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

func TestFunctionGroupAndBroadcastFlags(t *testing.T) {
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
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)

	if _, err := conn.Write(BuildGetSerialNumber()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}

	if _, err := conn.Write(BuildLAN(HeaderGetBroadcastFlags, nil)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	_, header, ok := PacketHeader(buf[:n])
	if !ok || header != HeaderGetBroadcastFlags {
		t.Fatalf("GET_BROADCAST header=%#x ok=%v", header, ok)
	}
	if flags := binary.LittleEndian.Uint32(buf[4:8]); flags != 0 {
		t.Fatalf("default flags=%#x", flags)
	}

	if _, err := conn.Write(BuildSetBroadcastFlags(BcDrivingSwitching | BcAllLocos)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(BuildLAN(HeaderGetBroadcastFlags, nil)); err != nil {
		t.Fatal(err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	want := BcDrivingSwitching | BcAllLocos
	if flags := binary.LittleEndian.Uint32(buf[4:8]); flags != want {
		t.Fatalf("stored flags=%#x want %#x", flags, want)
	}

	pkt := BuildSetLocoFunctionGroup(3, 0x20, 0x03) // F0+F1
	if _, err := conn.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(host.fns) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(host.fns) != 2 {
		t.Fatalf("group fns = %+v", host.fns)
	}

	got := make(chan LocoInfo, 1)
	go func() {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if info, ok := ParseLocoInfo(buf[:n]); ok {
			got <- info
		}
	}()
	srv.NotifyLocoState(drive.LocoState{Addr: 3, Speed: 20, Forward: true, Steps: 128, Functions: 1})
	select {
	case info := <-got:
		if info.Addr != 3 || info.Speed != 20 {
			t.Fatalf("notify info=%+v", info)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notify with flags set")
	}
}

func TestParseSetLocoFunctionGroup(t *testing.T) {
	pkt := BuildSetLocoFunctionGroup(3, 0x20, 0x11) // F0 + F4
	addr, lo, hi, bits, ok := ParseSetLocoFunctionGroup(pkt)
	if !ok || addr != 3 || lo != 0 || hi != 4 || bits != 0x11 {
		t.Fatalf("0x20: addr=%d lo=%d hi=%d bits=%#x ok=%v", addr, lo, hi, bits, ok)
	}
	pkt = BuildSetLocoFunctionGroup(10, 0x23, 0x81)
	addr, lo, hi, bits, ok = ParseSetLocoFunctionGroup(pkt)
	if !ok || addr != 10 || lo != 13 || hi != 20 || bits != 0x81 {
		t.Fatalf("0x23: addr=%d lo=%d hi=%d bits=%#x ok=%v", addr, lo, hi, bits, ok)
	}
}

func TestPeerLogoffReleasesHeld(t *testing.T) {
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
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	if _, err := conn.Write(BuildSetLocoDrive(3, 50, true, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(BuildLAN(HeaderLogoff, nil)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(host.released) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(host.released) != 1 || host.released[0].addr != 3 {
		t.Fatalf("released = %+v", host.released)
	}
}

func TestEvictStale(t *testing.T) {
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
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(BuildSetLocoDrive(7, 10, true, 3)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	srv.evictStale(time.Now().Add(peerTTL + time.Second))
	if len(host.released) != 1 || host.released[0].addr != 7 {
		t.Fatalf("evict released = %+v", host.released)
	}
}
