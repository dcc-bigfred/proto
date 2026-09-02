package commandstation

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// dialBinaryLoopback starts a loopback TCP server and connects a binary
// transport to it, returning the transport, the rx channel and the
// server-side connection.
func dialBinaryLoopback(t *testing.T) (*lnTCPBinaryTransport, chan lnPacket, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	type accepted struct {
		conn net.Conn
		err  error
	}
	accCh := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		accCh <- accepted{c, err}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	rxCh := make(chan lnPacket, 8)
	tr, err := newLnTCPBinaryTransport(addr.IP.String(), uint16(addr.Port), rxCh)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	acc := <-accCh
	if acc.err != nil {
		t.Fatalf("accept: %v", acc.err)
	}
	t.Cleanup(func() { _ = acc.conn.Close() })
	return tr, rxCh, acc.conn
}

func TestTCPBinaryReceivesRawBytes(t *testing.T) {
	tr, rxCh, srv := dialBinaryLoopback(t)

	// OPC_GPON (83 7C): a valid 2-byte message. Send it byte-by-byte to
	// prove the stream parser reassembles frames split across reads.
	if _, err := srv.Write([]byte{0x83}); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := srv.Write([]byte{0x7C}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case pkt := <-rxCh:
		if want := []byte{0x83, 0x7C}; !bytes.Equal(pkt, want) {
			t.Fatalf("got % X, want % X", []byte(pkt), want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for reassembled packet")
	}

	if tr.RxByteCount() != 2 {
		t.Fatalf("RxByteCount = %d, want 2", tr.RxByteCount())
	}
}

func TestTCPBinaryDropsBadChecksum(t *testing.T) {
	_, rxCh, srv := dialBinaryLoopback(t)

	// A0 05 10 00 is a complete 4-byte message with a wrong checksum
	// (correct would be 4A); it must be dropped, not forwarded.
	if _, err := srv.Write([]byte{0xA0, 0x05, 0x10, 0x00}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case pkt := <-rxCh:
		t.Fatalf("unexpected packet with bad checksum: % X", []byte(pkt))
	case <-time.After(150 * time.Millisecond):
		// expected: nothing forwarded
	}
}

func TestTCPBinaryWritesRawBytes(t *testing.T) {
	tr, _, srv := dialBinaryLoopback(t)

	msg := []byte{0xA0, 0x05, 0x10, 0x4A}
	if err := tr.WritePacket(msg); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	got := make([]byte, len(msg))
	_ = srv.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := readFull(srv, got); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("server got % X, want % X (binary transport must not ASCII-frame)", got, msg)
	}
}

// readFull reads len(buf) bytes from c.
func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
