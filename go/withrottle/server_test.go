package withrottle

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/commandstation"
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
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
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

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := fmt.Fprintln(conn, "HUtest"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	first, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimRight(first, "\r\n"); got != protocolVer {
		t.Fatalf("handshake first line = %q", got)
	}
	for i := 0; i < 8; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimRight(line, "\r\n"), "HT") {
			break
		}
	}

	if _, err := fmt.Fprintln(conn, "M0+S3<;>S3"); err != nil {
		t.Fatal(err)
	}
	ack, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimRight(ack, "\r\n"); got != "M0+S3<;>" {
		t.Fatalf("acquire ack = %q", got)
	}
	drainUntil(t, r, func(line string) bool {
		return strings.HasPrefix(line, "M0AS3<;>s")
	})

	if _, err := fmt.Fprintln(conn, "M0AS3<;>R1"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>V50"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(host.speeds) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.speeds) == 0 {
		t.Fatal("expected SetSpeed")
	}
	last := host.speeds[len(host.speeds)-1]
	if last.Addr != 3 || last.Speed != 50 || !last.Forward {
		t.Fatalf("speed = %+v", last)
	}

	if _, err := fmt.Fprintln(conn, "M0AS3<;>F10"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(host.fns) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.fns) != 1 || host.fns[0].fn != 0 || !host.fns[0].on {
		t.Fatalf("fns = %+v", host.fns)
	}
}

func TestClientLoopback(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	tcp := srv.Addr().(*net.TCPAddr)
	cli, err := NewClient("127.0.0.1", uint16(tcp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.CleanUp() })

	if err := cli.SetSpeed(3, 50, true, 128); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(host.speeds) > 0 && host.speeds[len(host.speeds)-1].Speed == 50 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.speeds) == 0 || host.speeds[len(host.speeds)-1].Addr != 3 {
		t.Fatalf("speeds = %+v", host.speeds)
	}

	if err := cli.SendFn(commandstation.MainTrackMode, 3, 0, false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(host.fns) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(host.fns) == 0 || host.fns[0].fn != 0 {
		t.Fatalf("fns = %+v", host.fns)
	}

	if _, err := cli.ReadCV(commandstation.MainTrackMode, commandstation.LocoCV{}); err != commandstation.ErrUnsupported {
		t.Fatalf("ReadCV err = %v", err)
	}
	if err := cli.WriteCV(commandstation.MainTrackMode, commandstation.LocoCV{}); err != commandstation.ErrUnsupported {
		t.Fatalf("WriteCV err = %v", err)
	}
}

func TestParseMAcquire(t *testing.T) {
	cmd, ok := ParseM("M0+S3<;>S3")
	if !ok || cmd.Op != MOpAdd || cmd.LocoKey != "S3" {
		t.Fatalf("ParseM: %+v ok=%v", cmd, ok)
	}
}

func drainUntil(t *testing.T, r *bufio.Reader, pred func(string) bool) {
	t.Helper()
	for i := 0; i < 40; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if pred(strings.TrimRight(line, "\r\n")) {
			return
		}
	}
	t.Fatal("timeout waiting for line")
}
