package commandstation

import (
	"bytes"
	"testing"
)

// recvLine drives a single LoconetOverTcp server line through the parser
// used by the TCP read loop and returns the packet pushed onto rxCh, if any.
func recvLine(t *testing.T, line string) (lnPacket, bool) {
	t.Helper()
	rxCh := make(chan lnPacket, 1)
	tr := &lnTCPASCIITransport{rxCh: rxCh, stop: make(chan struct{})}
	tr.handleLine(line)
	select {
	case pkt := <-rxCh:
		return pkt, true
	default:
		return nil, false
	}
}

func TestTCPHandleLineReceive(t *testing.T) {
	// A valid slot-speed message (A0 05 10 4A, checksum 0x4A) must reach rxCh.
	pkt, ok := recvLine(t, "RECEIVE A0 05 10 4A\r\n")
	if !ok {
		t.Fatalf("expected packet from RECEIVE line")
	}
	if want := []byte{0xA0, 0x05, 0x10, 0x4A}; !bytes.Equal(pkt, want) {
		t.Fatalf("got % X, want % X", []byte(pkt), want)
	}
}

func TestTCPHandleLineTimestampPrefix(t *testing.T) {
	// v2 servers may prefix a TIMESTAMP token on the same line; the parser
	// must still locate the RECEIVE payload (RocRail scans for it too).
	pkt, ok := recvLine(t, "TIMESTAMP 123456 RECEIVE 83 7C")
	if !ok {
		t.Fatalf("expected packet from TIMESTAMP-prefixed RECEIVE line")
	}
	if want := []byte{0x83, 0x7C}; !bytes.Equal(pkt, want) {
		t.Fatalf("got % X, want % X", []byte(pkt), want)
	}
}

func TestTCPHandleLineTrailingTokenTrimmed(t *testing.T) {
	// A verbose server may append extra tokens after the message. The
	// opcode's length code (A0 => 4 bytes) must bound the parse so the
	// trailing byte is ignored instead of breaking the checksum.
	pkt, ok := recvLine(t, "RECEIVE A0 05 10 4A 00")
	if !ok {
		t.Fatalf("expected packet despite trailing token")
	}
	if want := []byte{0xA0, 0x05, 0x10, 0x4A}; !bytes.Equal(pkt, want) {
		t.Fatalf("got % X, want % X", []byte(pkt), want)
	}
}

func TestTCPHandleLineNonReceiveIgnored(t *testing.T) {
	for _, line := range []string{"VERSION LbServer 1.0", "SENT OK", "ERROR LINE framing", ""} {
		if _, ok := recvLine(t, line); ok {
			t.Fatalf("line %q should not yield a packet", line)
		}
	}
}

func TestTCPHandleLineBadChecksumDropped(t *testing.T) {
	if _, ok := recvLine(t, "RECEIVE A0 05 10 00"); ok {
		t.Fatalf("packet with bad checksum must be dropped")
	}
}
