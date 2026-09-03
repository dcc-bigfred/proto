package commandstation

import (
	"testing"
)

func TestLnBuildLncvCvWrite63120(t *testing.T) {
	// JMRI: createCvWriteRequest(6312, 2, 3) for Uhlenbrock 63120 baud LNCV.
	got := lnBuildLncvCvWrite(6312, 2, 3)
	if len(got) != 15 || got[0] != lnOPC_IMM_PACKET || got[5] != lnLncvCmdWrite {
		t.Fatalf("unexpected write frame: % X", got)
	}
	if got[9] != 0x02 || got[11] != 0x03 {
		t.Fatalf("cv/value bytes wrong: % X", got)
	}
	if !lnChecksumOK(got) {
		t.Fatalf("checksum invalid: % X", got)
	}
}

func TestLnBuildLncvCvRead63120(t *testing.T) {
	got := lnBuildLncvCvRead(6312, 1, 2)
	if len(got) != 15 || got[0] != lnOPC_IMM_PACKET || got[5] != lnLncvCmdRead {
		t.Fatalf("unexpected read frame: % X", got)
	}
	if got[9] != 0x02 || got[11] != 0x01 {
		t.Fatalf("cv/module bytes wrong: % X", got)
	}
	if got[13] != 0x00 || got[6]&0x40 != 0 {
		t.Fatalf("read must not carry prog-on cmdData: % X", got)
	}
	if !lnChecksumOK(got) {
		t.Fatalf("checksum invalid: % X", got)
	}
}

func TestLnBuildLncvModProgStart63120(t *testing.T) {
	got := lnBuildLncvModProgStart(6312, 1)
	if !lnChecksumOK(got) {
		t.Fatalf("checksum invalid: % X", got)
	}
	if got[5] != lnLncvCmdRead {
		t.Fatalf("unexpected prog start cmd: % X", got)
	}
	if got[6]&0x40 == 0 {
		t.Fatalf("prog-on cmdData MSB missing in PXCT1: % X", got)
	}
	if got[11] != 0x01 {
		t.Fatalf("module address low byte wrong: % X", got)
	}
}

func TestParseLncvReadReply(t *testing.T) {
	raw := lnAppendChecksum([]byte{
		lnOPC_PEER_XFER, lnLncvLen, 0x05, 0x49, 0x4b, lnLncvCmdReadReply,
		0x01, 0x28, 0x18, 0x02, 0x00, 0x03, 0x00, 0x00,
	})
	if !lnChecksumOK(raw) {
		t.Fatalf("test vector checksum bad: % X", raw)
	}
	rep, ok := parseLncvReadReply(raw)
	if !ok {
		t.Fatalf("failed to parse read reply: % X", raw)
	}
	if rep.article != 6312 || rep.cv != 2 || rep.value != 3 {
		t.Fatalf("unexpected reply: %+v", rep)
	}
}

func TestClassifyLncvWriteAck(t *testing.T) {
	okPkt := lnAppendChecksum([]byte{lnOPC_LONG_ACK, lnLncvAckOpcImmPacket, lnLncvAckWriteOK})
	if classifyLncvWriteAck(okPkt) != lncvWriteAckOK {
		t.Fatalf("expected OK ack, got %v", classifyLncvWriteAck(okPkt))
	}
	retryPkt := lnAppendChecksum([]byte{lnOPC_LONG_ACK, lnLncvAckOpcImmPacket, 0x00})
	if classifyLncvWriteAck(retryPkt) != lncvWriteAckRetry {
		t.Fatalf("expected retry ack")
	}
}

func TestNormalizeLncvArticle(t *testing.T) {
	if got := NormalizeLncvArticle(63120); got != 6312 {
		t.Fatalf("expected 6312, got %d", got)
	}
	if got := NormalizeLncvArticle(6312); got != 6312 {
		t.Fatalf("expected 6312, got %d", got)
	}
}
