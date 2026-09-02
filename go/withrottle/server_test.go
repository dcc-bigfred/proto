package withrottle

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/drive"
)

type recHost struct {
	mu     sync.Mutex
	speeds []drive.LocoState
	fns    []struct {
		addr uint16
		fn   uint8
		on   bool
	}
	locos map[uint16]drive.LocoState
}

func (h *recHost) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, steps uint8) error {
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
func (h *recHost) SetFunction(_ drive.ClientID, addr uint16, fn uint8, on bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	h.mu.Lock()
	defer h.mu.Unlock()
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

func (h *recHost) fnsCopy() []struct {
	addr uint16
	fn   uint8
	on   bool
} {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]struct {
		addr uint16
		fn   uint8
		on   bool
	}, len(h.fns))
	copy(out, h.fns)
	return out
}

func (h *recHost) waitFns(t *testing.T, n int) []struct {
	addr uint16
	fn   uint8
	on   bool
} {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := h.fnsCopy()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d fns, got %+v", n, h.fnsCopy())
	return nil
}

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
		host.mu.Lock()
		n := len(host.speeds)
		host.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	host.mu.Lock()
	if len(host.speeds) == 0 {
		host.mu.Unlock()
		t.Fatal("expected SetSpeed")
	}
	last := host.speeds[len(host.speeds)-1]
	host.mu.Unlock()
	if last.Addr != 3 || last.Speed != 50 || !last.Forward {
		t.Fatalf("speed = %+v", last)
	}

	if _, err := fmt.Fprintln(conn, "M0AS3<;>F10"); err != nil {
		t.Fatal(err)
	}
	fns := host.waitFns(t, 1)
	if fns[0].fn != 0 || !fns[0].on {
		t.Fatalf("fns = %+v", fns)
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
		host.mu.Lock()
		ok := len(host.speeds) > 0 && host.speeds[len(host.speeds)-1].Speed == 50
		host.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	host.mu.Lock()
	if len(host.speeds) == 0 || host.speeds[len(host.speeds)-1].Addr != 3 {
		speeds := host.speeds
		host.mu.Unlock()
		t.Fatalf("speeds = %+v", speeds)
	}
	host.mu.Unlock()

	if err := cli.SendFn(3, 0, false); err != nil {
		t.Fatal(err)
	}
	fns := host.waitFns(t, 1)
	if fns[0].fn != 0 {
		t.Fatalf("fns = %+v", fns)
	}

	if _, err := cli.ReadCV(); err != ErrUnsupported {
		t.Fatalf("ReadCV err = %v", err)
	}
	if err := cli.WriteCV(); err != ErrUnsupported {
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

func wtDial(t *testing.T, srv *Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintln(conn, "HUtest"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	drainUntil(t, r, func(line string) bool { return strings.HasPrefix(line, "HT") })
	if _, err := fmt.Fprintln(conn, "M0+S3<;>S3"); err != nil {
		t.Fatal(err)
	}
	drainUntil(t, r, func(line string) bool { return strings.HasPrefix(line, "M0AS3<;>s") })
	return conn, r
}

func TestFunctionPressLatchingF1(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, r := wtDial(t, srv)

	if _, err := fmt.Fprintln(conn, "M0AS3<;>F11"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F01"); err != nil {
		t.Fatal(err)
	}
	fns := host.waitFns(t, 1)
	if len(fns) != 1 || fns[0].fn != 1 || !fns[0].on {
		t.Fatalf("first pair: %+v", fns)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F11"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F01"); err != nil {
		t.Fatal(err)
	}
	fns = host.waitFns(t, 2)
	if len(fns) != 2 || fns[1].fn != 1 || fns[1].on {
		t.Fatalf("second pair: %+v", fns)
	}
	_ = r
}

func TestFunctionPressMomentaryF2(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, _ := wtDial(t, srv)
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F12"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F02"); err != nil {
		t.Fatal(err)
	}
	fns := host.waitFns(t, 2)
	if !fns[0].on || fns[1].on || fns[0].fn != 2 || fns[1].fn != 2 {
		t.Fatalf("F2 press/release: %+v", fns)
	}
}

func TestFunctionModeOverride(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, _ := wtDial(t, srv)
	if _, err := fmt.Fprintln(conn, "M0AS3<;>m11"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F11"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F01"); err != nil {
		t.Fatal(err)
	}
	fns := host.waitFns(t, 2)
	if !fns[0].on || fns[1].on || fns[0].fn != 1 {
		t.Fatalf("m11 F1 momentary: %+v", fns)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>m02"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F12"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F02"); err != nil {
		t.Fatal(err)
	}
	fns = host.waitFns(t, 3)
	if len(fns) != 3 || fns[2].fn != 2 || !fns[2].on {
		t.Fatalf("m02 F2 latching: %+v", fns)
	}
}

func TestFunctionForceNoChangeEcho(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, r := wtDial(t, srv)
	if _, err := fmt.Fprintln(conn, "M0AS3<;>f11"); err != nil {
		t.Fatal(err)
	}
	echo := ""
	drainUntil(t, r, func(line string) bool {
		if line == "M0AS3<;>F11" {
			echo = line
			return true
		}
		return false
	})
	if echo == "" {
		t.Fatal("expected echo")
	}
	fns := host.waitFns(t, 1)
	if _, err := fmt.Fprintln(conn, "M0AS3<;>f11"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := host.fnsCopy(); len(got) != 1 {
		t.Fatalf("second force should be no-op, got %+v (first %+v)", got, fns)
	}
}

type recHostMom struct {
	recHost
	mom uint8
}

func (h *recHostMom) Momentary(_ uint16, fn uint8) bool { return fn == h.mom }

func TestFunctionModerAndSessionOverride(t *testing.T) {
	host := &recHostMom{mom: 3}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, _ := wtDial(t, srv)
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F13"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F03"); err != nil {
		t.Fatal(err)
	}
	fns := host.waitFns(t, 2)
	if fns[0].fn != 3 || !fns[0].on || fns[1].on {
		t.Fatalf("host Moder F3: %+v", fns)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>m03"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F13"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(conn, "M0AS3<;>F03"); err != nil {
		t.Fatal(err)
	}
	fns = host.waitFns(t, 3)
	if len(fns) != 3 {
		t.Fatalf("session m03 should latch, got %+v", fns)
	}
}

func TestNotifyNotBlockedByStalledPeer(t *testing.T) {
	host := &recHost{}
	srv, err := Listen("127.0.0.1:0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	stalled, _ := wtDial(t, srv)
	_ = stalled.SetDeadline(time.Time{})
	good, r := wtDial(t, srv)
	_ = good.SetDeadline(time.Now().Add(2 * time.Second))

	done := make(chan struct{})
	go func() {
		srv.NotifyLocoState(drive.LocoState{Addr: 3, Speed: 40, Forward: true, Steps: 128, Functions: 1})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyLocoState blocked")
	}
	saw := false
	drainUntil(t, r, func(line string) bool {
		if strings.Contains(line, "V40") {
			saw = true
			return true
		}
		return false
	})
	if !saw {
		t.Fatal("well-behaved peer got no notify")
	}
}

func TestClientAcquireTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for i := 0; i < 3; i++ {
			_, _ = r.ReadString('\n')
		}
		_, _ = fmt.Fprintln(conn, "VN2.0")
		_, _ = fmt.Fprintln(conn, "*10")
		_, _ = fmt.Fprintln(conn, "PPA1")
		_, _ = fmt.Fprintln(conn, "RL0")
		_, _ = fmt.Fprintln(conn, "HTdummy")
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}()
	tcp := ln.Addr().(*net.TCPAddr)
	cli, err := NewClient("127.0.0.1", uint16(tcp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.CleanUp() })
	err = cli.SetSpeed(3, 50, true, 128)
	if err != ErrAcquireTimeout {
		t.Fatalf("SetSpeed err = %v, want ErrAcquireTimeout", err)
	}
}

func TestParsePressForceMode(t *testing.T) {
	fn, pressed, ok := parsePress("F11")
	if !ok || fn != 1 || !pressed {
		t.Fatalf("parsePress F11: %d %v %v", fn, pressed, ok)
	}
	fn, on, ok := parseForce("f10")
	if !ok || fn != 0 || !on {
		t.Fatalf("parseForce f10: %d %v %v", fn, on, ok)
	}
	fn, mom, ok := parseMode("m12")
	if !ok || fn != 2 || !mom {
		t.Fatalf("parseMode m12: %d %v %v", fn, mom, ok)
	}
}
