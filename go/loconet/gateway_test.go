package loconet

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

var gpon = []byte{0x83, 0x7C}

func TestChecksumGPON(t *testing.T) {
	if !ChecksumOK(gpon) {
		t.Fatal("GPON checksum")
	}
	if got := AppendChecksum([]byte{0x83}); !bytes.Equal(got, gpon) {
		t.Fatalf("AppendChecksum = % X", got)
	}
}

func TestGatewayBinaryFanout(t *testing.T) {
	up := NewRecorder()
	gw := NewGateway(up)
	t.Cleanup(func() { _ = gw.Close() })

	addr, err := gw.Listen("127.0.0.1:0", Binary)
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
		up.mu.Lock()
		n := len(up.Written)
		up.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.Written) == 0 || !bytes.Equal(up.Written[0], gpon) {
		t.Fatalf("upstream written = % X", up.Written)
	}
}

func TestGatewayASCIIFanout(t *testing.T) {
	gw := NewGateway(nil)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", ASCII)
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
	gw := NewGateway(nil)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", Binary)
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
	gw := NewGateway(nil)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", ASCII)
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
	up := NewRecorder()
	gw := NewGateway(up)
	t.Cleanup(func() { _ = gw.Close() })
	addr, err := gw.Listen("127.0.0.1:0", Binary)
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
