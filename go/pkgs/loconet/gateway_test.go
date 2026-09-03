package loconet_test

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/commandstation"
	"github.com/dcc-bigfred/proto/go/pkgs/loconet"
)

var gpon = []byte{0x83, 0x7C}

func TestChecksumGPON(t *testing.T) {
	if !loconet.ChecksumOK(gpon) {
		t.Fatal("GPON checksum")
	}
	if got := loconet.AppendChecksum([]byte{0x83}); !bytes.Equal(got, gpon) {
		t.Fatalf("AppendChecksum = % X", got)
	}
}

func TestGatewayBinaryFanout(t *testing.T) {
	up := loconet.NewRecorder()
	gw := loconet.NewGateway(up)
	t.Cleanup(func() { _ = gw.Close() })

	addr, err := gw.Listen("127.0.0.1:0", loconet.Binary)
	if err != nil {
		t.Fatal(err)
	}

	a, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	time.Sleep(20 * time.Millisecond)

	if _, err := a.Write(gpon); err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], gpon) {
		t.Fatalf("peer got % X", buf[:n])
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(up.WrittenPackets()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := up.WrittenPackets()
	if len(got) == 0 || !bytes.Equal(got[0], gpon) {
		t.Fatalf("upstream written = % X", got)
	}
}

func TestGatewayASCIIFanout(t *testing.T) {
	gw := loconet.NewGateway(nil)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", loconet.ASCII)
	if err != nil {
		t.Fatal(err)
	}

	a, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	ra := bufio.NewReader(a)
	rb := bufio.NewReader(b)
	_ = a.SetDeadline(time.Now().Add(3 * time.Second))
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	if ver, err := ra.ReadString('\n'); err != nil || !strings.HasPrefix(ver, "VERSION") {
		t.Fatalf("a VERSION: %q %v", ver, err)
	}
	if ver, err := rb.ReadString('\n'); err != nil || !strings.HasPrefix(ver, "VERSION") {
		t.Fatalf("b VERSION: %q %v", ver, err)
	}

	if _, err := a.Write([]byte("SEND 83 7C\r\n")); err != nil {
		t.Fatal(err)
	}
	sent, err := ra.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(sent) != "SENT OK" {
		t.Fatalf("SENT: %q", sent)
	}
	recv, err := rb.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(recv); got != "RECEIVE 83 7C" {
		t.Fatalf("RECEIVE: %q", got)
	}
}

func TestGatewayBinaryClientDials(t *testing.T) {
	gw := loconet.NewGateway(nil)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", loconet.Binary)
	if err != nil {
		t.Fatal(err)
	}
	tcp := addr.(*net.TCPAddr)
	st, err := commandstation.NewLocoNetTCPBinary("127.0.0.1", uint16(tcp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.CleanUp() })
}

func TestGatewayASCIIClientDials(t *testing.T) {
	gw := loconet.NewGateway(nil)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", loconet.ASCII)
	if err != nil {
		t.Fatal(err)
	}
	tcp := addr.(*net.TCPAddr)
	st, err := commandstation.NewLocoNetTCP("127.0.0.1", uint16(tcp.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.CleanUp() })
}

func TestUpstreamInject(t *testing.T) {
	up := loconet.NewRecorder()
	gw := loconet.NewGateway(up)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", loconet.Binary)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	time.Sleep(20 * time.Millisecond)
	up.Inject(gpon)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], gpon) {
		t.Fatalf("got % X", buf[:n])
	}
}

func TestGatewayCloseIdempotent(t *testing.T) {
	up := loconet.NewRecorder()
	gw := loconet.NewGateway(up)
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := up.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenLocoNetTCPBinaryAsUpstream(t *testing.T) {
	phys := loconet.NewGateway(nil)
	t.Cleanup(func() { _ = phys.Close() })
	physAddr, err := phys.Listen("127.0.0.1:0", loconet.Binary)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := net.Dial("tcp", physAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	time.Sleep(20 * time.Millisecond)

	tcp := physAddr.(*net.TCPAddr)
	up, err := commandstation.OpenLocoNetTCPBinary("127.0.0.1", uint16(tcp.Port))
	if err != nil {
		t.Fatal(err)
	}
	gw := loconet.NewGateway(up)
	t.Cleanup(func() { _ = gw.Close() })
	downAddr, err := gw.Listen("127.0.0.1:0", loconet.Binary)
	if err != nil {
		t.Fatal(err)
	}
	d, err := net.Dial("tcp", downAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	time.Sleep(20 * time.Millisecond)

	if _, err := d.Write(gpon); err != nil {
		t.Fatal(err)
	}
	_ = observer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	n, err := observer.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], gpon) {
		t.Fatalf("physical observer got % X", buf[:n])
	}
}
