package withrottle

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

type gateHost struct {
	recHost
	proceed     bool
	custom      []string
	actionOK    bool
	ppaHandled  bool
	nConsumed   bool
	connects    []drive.ClientID
	activities  int
	quits       int
	disconnects int
	subs        []uint16
	mu          sync.Mutex
}

func (h *gateHost) Acquire(_ drive.ClientID, _ byte, _ uint16) (bool, []string) {
	return h.proceed, h.custom
}
func (h *gateHost) Action(drive.ClientID, byte, string, uint16, string) bool { return h.actionOK }
func (h *gateHost) TrackPower(drive.ClientID, bool) bool                     { return h.ppaHandled }
func (h *gateHost) Subscribe(_ drive.ClientID, addr uint16) error {
	h.mu.Lock()
	h.subs = append(h.subs, addr)
	h.mu.Unlock()
	return nil
}
func (h *gateHost) OnConnect(id drive.ClientID, _ string) {
	h.mu.Lock()
	h.connects = append(h.connects, id)
	h.mu.Unlock()
}
func (h *gateHost) OnActivity(drive.ClientID) {
	h.mu.Lock()
	h.activities++
	h.mu.Unlock()
}
func (h *gateHost) OnQuit(drive.ClientID) {
	h.mu.Lock()
	h.quits++
	h.mu.Unlock()
}
func (h *gateHost) OnDisconnect(drive.ClientID) {
	h.mu.Lock()
	h.disconnects++
	h.mu.Unlock()
}
func (h *gateHost) OnN(drive.ClientID, string) bool { return h.nConsumed }

type staticRoster []RosterEntry

func (r staticRoster) Roster(drive.ClientID) []RosterEntry { return r }

func TestAcquireGateVetoSkipsLocoState(t *testing.T) {
	h := &gateHost{proceed: false, custom: []string{"HMNot paired"}}
	srv, err := Listen("127.0.0.1:0", h)
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
	fmt.Fprintln(conn, "HUgate")
	r := bufio.NewReader(conn)
	drainUntil(t, r, func(line string) bool { return strings.HasPrefix(line, "HT") })
	fmt.Fprintln(conn, "M0+S3<;>S3")
	got := ""
	drainUntil(t, r, func(line string) bool {
		if line == "HMNot paired" {
			got = line
			return true
		}
		return false
	})
	if got != "HMNot paired" {
		t.Fatalf("got %q", got)
	}
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("Subscribe called %d times", n)
	}
}

func TestActionGateSkipsSetFunction(t *testing.T) {
	h := &gateHost{proceed: true, actionOK: true}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, r := wtDialHU(t, srv, "act")
	_ = r
	fmt.Fprintln(conn, "M0AS3<;>F10")
	time.Sleep(80 * time.Millisecond)
	if fns := h.fnsCopy(); len(fns) != 0 {
		t.Fatalf("SetFunction should be skipped, got %+v", fns)
	}
}

func TestTrackPowerGateSkipsBroadcast(t *testing.T) {
	h := &gateHost{proceed: true, ppaHandled: true}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, r := wtDialHU(t, srv, "ppa")
	fmt.Fprintln(conn, "PPA0")
	time.Sleep(50 * time.Millisecond)
	_ = conn.SetDeadline(time.Now().Add(150 * time.Millisecond))
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("unexpected PPA broadcast")
	}
}

func TestNHookConsumedNoHeartbeatReply(t *testing.T) {
	h := &gateHost{proceed: true, nConsumed: true}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintln(conn, "HUnhook")
	r := bufio.NewReader(conn)
	drainUntil(t, r, func(line string) bool { return strings.HasPrefix(line, "HT") })
	fmt.Fprintln(conn, "N123456")
	_ = conn.SetDeadline(time.Now().Add(150 * time.Millisecond))
	if line, err := r.ReadString('\n'); err == nil {
		t.Fatalf("unexpected reply %q", strings.TrimRight(line, "\r\n"))
	}
}

func TestRosterProviderAndResendBurst(t *testing.T) {
	h := &gateHost{proceed: true}
	srv, err := Listen("127.0.0.1:0", h,
		WithServerName("BigFred"),
		WithRosterProvider(staticRoster{{Name: "Pair with BigFred", Addr: 3}}),
	)
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
	fmt.Fprintln(conn, "HUros")
	r := bufio.NewReader(conn)
	sawRL, sawHT := false, false
	for i := 0; i < 8; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "RL1") && strings.Contains(line, "Pair with BigFred") {
			sawRL = true
		}
		if line == "HTBigFred" {
			sawHT = true
			break
		}
	}
	if !sawRL || !sawHT {
		t.Fatalf("burst RL=%v HT=%v", sawRL, sawHT)
	}
	srv.SendTo(drive.ClientID("withrottle:ros"), "HmPaired as 1")
	got, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(got, "\r\n") != "HmPaired as 1" {
		t.Fatalf("SendTo = %q", got)
	}
}

func TestNotifyLocoStateExceptSkipsOrigin(t *testing.T) {
	h := &recHost{}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	origin, or := wtDialHU(t, srv, "origin")
	_, pr := wtDialHU(t, srv, "peer")
	_ = origin
	srv.NotifyLocoStateExcept(drive.LocoState{Addr: 3, Speed: 40, Forward: true, Steps: 128}, "withrottle:origin")
	sawPeer, sawOrigin := false, false
	_ = origin.SetDeadline(time.Now().Add(200 * time.Millisecond))
	drainUntil(t, pr, func(line string) bool {
		if strings.Contains(line, "V40") {
			sawPeer = true
			return true
		}
		return false
	})
	if _, err := or.ReadString('\n'); err == nil {
		sawOrigin = true
	}
	if !sawPeer {
		t.Fatal("peer got no notify")
	}
	if sawOrigin {
		t.Fatal("origin should be skipped")
	}
}

func TestHUTakeoverClosesPriorConn(t *testing.T) {
	h := &gateHost{proceed: true}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	first, _ := net.Dial("tcp", srv.Addr().String())
	t.Cleanup(func() { _ = first.Close() })
	_ = first.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintln(first, "HUsame")
	fr := bufio.NewReader(first)
	drainUntil(t, fr, func(line string) bool { return strings.HasPrefix(line, "HT") })
	second, _ := net.Dial("tcp", srv.Addr().String())
	t.Cleanup(func() { _ = second.Close() })
	_ = second.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintln(second, "HUsame")
	sr := bufio.NewReader(second)
	drainUntil(t, sr, func(line string) bool { return strings.HasPrefix(line, "HT") })
	buf := make([]byte, 1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = first.SetDeadline(time.Now().Add(50 * time.Millisecond))
		_, err := first.Read(buf)
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue
		}
		return
	}
	t.Fatal("first connection should be closed after takeover")
}

func TestFormatRosterLineSorted(t *testing.T) {
	got := FormatRosterLine([]RosterEntry{{Name: "B", Addr: 200}, {Name: "A", Addr: 3}})
	want := `RL2]\[A}|{3}|{S]\[B}|{200}|{L`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDccSpeedWireRoundTrip(t *testing.T) {
	if DccSpeedFromWire(1, 128) != 0 {
		t.Fatal("wire 1 is estop payload → DCC 0")
	}
	if WireSpeedFromDCC(0, 128) != 0 {
		t.Fatal("DCC 0 → wire 0")
	}
	w := WireSpeedFromDCC(50, 128)
	if w < 2 || w > 126 {
		t.Fatalf("wire %d", w)
	}
	if DccSpeedFromWire(0, 128) != 0 {
		t.Fatal("wire 0")
	}
}

func TestQuitCallsOnQuit(t *testing.T) {
	h := &gateHost{proceed: true}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintln(conn, "HUquit")
	r := bufio.NewReader(conn)
	drainUntil(t, r, func(line string) bool { return strings.HasPrefix(line, "HT") })
	fmt.Fprintln(conn, "Q")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		q := h.quits
		h.mu.Unlock()
		if q >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OnQuit not called")
}
