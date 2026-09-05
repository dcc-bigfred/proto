package z21

import (
	"net"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/drive"
)

type z21GateHost struct {
	recHost
	driveHandled bool
	cvHandled    bool
	cvReply      []byte
	logoffs      int
}

func (h *z21GateHost) Drive(drive.ClientID, DriveOp, uint16, []byte) bool { return h.driveHandled }
func (h *z21GateHost) CV(drive.ClientID, CVOp, uint16, uint16, uint8) (bool, []byte) {
	return h.cvHandled, h.cvReply
}
func (h *z21GateHost) OnActivity(drive.ClientID)              {}
func (h *z21GateHost) OnLogoff(drive.ClientID)                { h.logoffs++ }
func (h *z21GateHost) OnBroadcastFlags(drive.ClientID, uint32) {}

func TestDriveGateSkipsSetSpeed(t *testing.T) {
	h := &z21GateHost{driveHandled: true}
	srv, err := Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, err := net.DialUDP("udp", nil, srv.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write(BuildSetLocoDrive(3, 50, true, 3)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(h.speeds) != 0 {
		t.Fatalf("SetSpeed should be skipped, got %+v", h.speeds)
	}
}

func TestCVGatePomWrite(t *testing.T) {
	h := &z21GateHost{cvHandled: true, cvReply: BuildCvResult(3, 0x12)}
	srv, err := Listen("127.0.0.1:0", h)
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
	if _, err := conn.Write(BuildPomWriteByte(3, 2, 0x12)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ParseCvReply(buf[:n])
	if !ok || got.Kind != CvResult || got.Value != 0x12 {
		t.Fatalf("cv reply %+v ok=%v", got, ok)
	}
}

func TestLogoffCallsHook(t *testing.T) {
	h := &z21GateHost{}
	srv, err := Listen("127.0.0.1:0", h)
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
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = conn.Read(buf)
	if _, err := conn.Write(BuildLAN(HeaderLogoff, nil)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.logoffs >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OnLogoff not called")
}

func TestClientKeyFuncIPOnly(t *testing.T) {
	h := &recHost{}
	srv, err := Listen("127.0.0.1:0", h, WithClientKeyFunc(func(a *net.UDPAddr) drive.ClientID {
		return drive.ClientID(a.IP.String())
	}))
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
	if _, err := conn.Write(BuildSetLocoDrive(3, 40, true, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	id := drive.ClientID(conn.LocalAddr().(*net.UDPAddr).IP.String())
	if srv.BroadcastFlags(id) != 0 {
		// just prove the key is IP-only and peer exists
	}
	if _, ok := srv.peers[id]; !ok {
		t.Fatalf("peer not keyed by IP, have %v", srv.peers)
	}
}

func TestParseSetStopAndPom(t *testing.T) {
	if !ParseSetStop(xbus([]byte{0x80, 0x80})) {
		t.Fatal("set stop")
	}
	pkt := BuildPomWriteByte(10, 2, 7)
	addr, cv, val, ok := ParsePomWriteByte(pkt)
	if !ok || addr != 10 || cv != 2 || val != 7 {
		t.Fatalf("pom write %d %d %d %v", addr, cv, val, ok)
	}
}
