package commandstation

import (
	"bytes"
	"testing"
)

func TestLnChecksum(t *testing.T) {
	// Example from LoconetOverTcp docs: RECEIVE 83 7C
	pkt := []byte{0x83, 0x7C}
	if !lnChecksumOK(pkt) {
		t.Fatalf("expected checksum OK for % X", pkt)
	}
}

func TestLnFixedLen(t *testing.T) {
	// 0x83 => class with len=2 (bits5..6 = 00)
	l, ok := lnMsgLen(0x83, []byte{0x83})
	if !ok || l != 2 {
		t.Fatalf("expected len=2, got %d ok=%v", l, ok)
	}
	// 0xA0 => 4-byte
	l, ok = lnMsgLen(0xA0, []byte{0xA0})
	if !ok || l != 4 {
		t.Fatalf("expected len=4 for 0xA0, got %d ok=%v", l, ok)
	}
}

func TestLnVarLen(t *testing.T) {
	// SL_RD_DATA is variable, second byte is 0x0E
	buf := []byte{lnOPC_SL_RD_DATA, 0x0E}
	l, ok := lnMsgLen(buf[0], buf)
	if !ok || l != 14 {
		t.Fatalf("expected len=14, got %d ok=%v", l, ok)
	}
}

func TestParseSlotData(t *testing.T) {
	// Construct a minimal valid E7 0E message with checksum.
	// Slot=3, addr=5, speed=10, dirf=0x20 (forward), snd=0x02 (F6 on).
	msg := []byte{
		lnOPC_SL_RD_DATA, 0x0E,
		0x03, // slot
		0x00, // stat1
		0x05, // adr low
		0x0A, // speed
		0x20, // dirf
		0x00, // trk
		0x00, // stat2
		0x00, // adr high
		0x02, // snd
		0x00, // id1
		0x00, // id2
	}
	pkt := lnAppendChecksum(msg)
	sd, ok := parseLnSlotData(pkt)
	if !ok {
		t.Fatalf("expected parse ok for % X", pkt)
	}
	if sd.Slot != 0x03 || sd.Addr != 5 || sd.Speed != 0x0A || sd.DirF != 0x20 || sd.Snd != 0x02 {
		t.Fatalf("unexpected slot data: %+v", sd)
	}
}

func TestLnStreamParserMidFrameResync(t *testing.T) {
	var p lnStreamParser
	if _, ok := p.PushByte(0xA0); ok {
		t.Fatal("expected incomplete 4-byte frame")
	}
	if _, ok := p.PushByte(0xBF); ok {
		t.Fatal("expected incomplete after resync")
	}
	if len(p.cur) != 1 || p.cur[0] != 0xBF {
		t.Fatalf("expected resync on opcode byte, cur=% X", p.cur)
	}
}

func TestLnStreamParserCompletesGpon(t *testing.T) {
	var p lnStreamParser
	if _, ok := p.PushByte(0x83); ok {
		t.Fatal("expected incomplete 2-byte frame")
	}
	pkt, ok := p.PushByte(0x7C)
	if !ok || len(pkt) != 2 || pkt[0] != 0x83 {
		t.Fatalf("got % X ok=%v", pkt, ok)
	}
}

func TestLnStreamParserRejectsOversizeLength(t *testing.T) {
	var p lnStreamParser
	// Variable opcode with illegal length byte 0x00.
	if _, ok := p.PushByte(0xE7); ok {
		t.Fatal("incomplete")
	}
	if _, ok := p.PushByte(0x00); ok {
		t.Fatal("expected reject for zero length")
	}
	if len(p.cur) != 0 {
		t.Fatalf("parser should reset on bad length, cur=% X", p.cur)
	}
}

func TestLnBuildLocoAdr(t *testing.T) {
	// OPC_LOCO_ADR must be a 4-byte message: BF <ADR_HI> <ADR_LO> <CHK>.
	// A malformed 5-byte frame is silently rejected by the command station
	// (no E7 reply), which times out every slot allocation.
	pkt := lnBuildLocoAdr(3)
	if want := []byte{0xBF, 0x00, 0x03, 0x43}; !bytes.Equal(pkt, want) {
		t.Fatalf("loco 3: got % X, want % X", pkt, want)
	}
	if n, ok := lnMsgLen(pkt[0], pkt); !ok || n != 4 || len(pkt) != 4 {
		t.Fatalf("expected a 4-byte frame, got len=%d (lnMsgLen=%d ok=%v)", len(pkt), n, ok)
	}
	if !lnChecksumOK(pkt) {
		t.Fatalf("checksum invalid: % X", pkt)
	}

	// Long address splits into 7-bit high/low halves.
	long := lnBuildLocoAdr(1234) // 1234 = hi 0x09, lo 0x52
	if want := []byte{0xBF, 0x09, 0x52}; !bytes.Equal(long[:3], want) {
		t.Fatalf("loco 1234: got % X, want prefix % X", long, want)
	}
	if len(long) != 4 || !lnChecksumOK(long) {
		t.Fatalf("loco 1234: bad frame % X", long)
	}
}

func TestDccAddrBytes(t *testing.T) {
	if got := dccAddrBytes(3); !bytes.Equal(got, []byte{0x03}) {
		t.Fatalf("short addr 3: got % X", got)
	}
	// 1234 = 0x04D2 -> C0|0x04=0xC4, 0xD2
	if got := dccAddrBytes(1234); !bytes.Equal(got, []byte{0xC4, 0xD2}) {
		t.Fatalf("long addr 1234: got % X", got)
	}
}

func TestDccFnGroupPacket(t *testing.T) {
	// F9 on, short address 3 -> [0x03, 0xA1]
	pkt, ok := dccFnGroupPacket(3, 9, 1<<9)
	if !ok || !bytes.Equal(pkt, []byte{0x03, 0xA1}) {
		t.Fatalf("F9 group: ok=%v pkt=% X", ok, pkt)
	}
	// F12 on -> mask bit3 -> 0xA8
	pkt, _ = dccFnGroupPacket(3, 12, 1<<12)
	if !bytes.Equal(pkt, []byte{0x03, 0xA8}) {
		t.Fatalf("F12 group: pkt=% X", pkt)
	}
	// F13 on -> [0x03, 0xDE, 0x01]
	pkt, _ = dccFnGroupPacket(3, 13, 1<<13)
	if !bytes.Equal(pkt, []byte{0x03, 0xDE, 0x01}) {
		t.Fatalf("F13 group: pkt=% X", pkt)
	}
	// F28 on, long address 1234 -> [0xC4, 0xD2, 0xDF, 0x80]
	pkt, _ = dccFnGroupPacket(1234, 28, 1<<28)
	if !bytes.Equal(pkt, []byte{0xC4, 0xD2, 0xDF, 0x80}) {
		t.Fatalf("F28 long group: pkt=% X", pkt)
	}
	// F8 is not an extended group
	if _, ok := dccFnGroupPacket(3, 8, 0); ok {
		t.Fatalf("F8 should not produce an extended group packet")
	}
}

func TestImmPacketRoundTrip(t *testing.T) {
	for _, addr := range []LocoAddr{3, 1234} {
		for _, fn := range []int{9, 12, 13, 20, 21, 28} {
			dcc, ok := dccFnGroupPacket(addr, fn, 1<<uint(fn))
			if !ok {
				t.Fatalf("no group for F%d", fn)
			}
			imm, err := lnBuildImmPacket(dcc, lnImmRepeats)
			if err != nil {
				t.Fatalf("build imm F%d: %v", fn, err)
			}
			if !lnChecksumOK(imm) {
				t.Fatalf("imm checksum invalid: % X", imm)
			}
			// Length class is variable; the byte-count must be 0x0B.
			if imm[1] != 0x0B {
				t.Fatalf("imm length byte: got 0x%02X", imm[1])
			}
			back := decodeImmDccPacket(imm)
			if !bytes.Equal(back, dcc) {
				t.Fatalf("imm roundtrip F%d: in=% X out=% X", fn, dcc, back)
			}
			gotAddr, fns, ok := dccPacketFunctions(back)
			if !ok || gotAddr != addr {
				t.Fatalf("decode F%d: ok=%v addr=%d (want %d)", fn, ok, gotAddr, addr)
			}
			if on, present := fns[fn]; !present || !on {
				t.Fatalf("decode F%d: function not reported on, fns=%v", fn, fns)
			}
		}
	}
}

func TestImmF9DocExample(t *testing.T) {
	// Documented example (repeats=2, JMRI-style DHI without the 0x20 bit):
	// F9 ON, short address 3 -> ED 0B 7F 21 02 03 21 ...
	dcc, _ := dccFnGroupPacket(3, 9, 1<<9)
	imm, err := lnBuildImmPacket(dcc, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xED, 0x0B, 0x7F, 0x21, 0x02, 0x03, 0x21, 0x00, 0x00, 0x00}
	if !bytes.Equal(imm[:10], want) {
		t.Fatalf("imm header mismatch:\n got % X\nwant % X", imm[:10], want)
	}
}

func TestProgTaskAndReply(t *testing.T) {
	// CV1 (0-based 0), read direct byte.
	task := lnBuildProgTask(lnPCMD_READ_DIRECT, 0, 0)
	if !lnChecksumOK(task) {
		t.Fatalf("prog task checksum invalid: % X", task)
	}
	want := []byte{lnOPC_WR_SL_DATA, 0x0E, lnPRG_SLOT, lnPCMD_READ_DIRECT, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x7F, 0x7F}
	if !bytes.Equal(task[:13], want) {
		t.Fatalf("prog task mismatch:\n got % X\nwant % X", task[:13], want)
	}

	// CV1000 (0-based 999): CVH=0x31, CVL=0x67.
	task = lnBuildProgTask(lnPCMD_READ_DIRECT, 999, 0)
	if task[8] != 0x31 || task[9] != 0x67 {
		t.Fatalf("CV999 encoding: CVH=0x%02X CVL=0x%02X", task[8], task[9])
	}

	// Reply carrying value 200 (0xC8): data7=0x48, CVH data-MSB bit set.
	reply := lnAppendChecksum([]byte{
		lnOPC_SL_RD_DATA, 0x0E, lnPRG_SLOT,
		lnPCMD_READ_DIRECT, 0x00, // pcmd, pstat
		0x00, 0x00, 0x00, // hopsa, lopsa, trk
		0x02,       // CVH: data MSB (bit1) set
		0x00,       // CVL
		0x48,       // DATA7 (low 7 bits of 200)
		0x7F, 0x7F, // id1, id2
	})
	rep, ok := parseLnProgReply(reply)
	if !ok {
		t.Fatalf("parse prog reply failed: % X", reply)
	}
	if rep.PStat != 0 || rep.Value != 200 {
		t.Fatalf("prog reply: pstat=0x%02X value=%d (want value 200)", rep.PStat, rep.Value)
	}
	if err := lnProgStatusError(rep.PStat); err != nil {
		t.Fatalf("unexpected pstat error: %v", err)
	}

	// A no-decoder PSTAT must surface as an error.
	if err := lnProgStatusError(lnPSTAT_NO_DECODER); err == nil {
		t.Fatalf("expected error for no-decoder PSTAT")
	}
}

func TestParseSlotDataStat1(t *testing.T) {
	// STAT1 uses D5/D4 for occupancy; D2–D0 are decoder speed-step mode.
	// 0x33 = IN_USE (0x30) | 128-step (0x03).
	msg := []byte{
		lnOPC_SL_RD_DATA, 0x0E,
		0x05,       // slot
		0x33,       // stat1: IN_USE + 128-step
		0x07,       // adr lo
		0x15,       // speed
		0x20,       // dirf
		0x00,       // trk
		0x00,       // stat2
		0x00,       // adr hi
		0x00,       // snd
		0x00, 0x00, // id1, id2
	}
	pkt := lnAppendChecksum(msg)
	sd, ok := parseLnSlotData(pkt)
	if !ok {
		t.Fatalf("expected parse ok for % X", pkt)
	}
	if sd.Stat1 != 0x33 {
		t.Fatalf("Stat1: got 0x%02X, want 0x33", sd.Stat1)
	}
	if sd.Stat1&lnSLOT_STA_MASK != lnSLOT_IN_USE {
		t.Fatalf("SL_STA: got 0x%02X, want lnSLOT_IN_USE (0x30)", sd.Stat1&lnSLOT_STA_MASK)
	}
}

// COMMON with 128-step mode is STAT1 0x13. Masking with the wrong low bits
// (0x03) would mis-read that as IN_USE and reject every free 128-step loco.
func TestSlotStatusMaskIgnoresDecoderModeBits(t *testing.T) {
	const dec128 = byte(0x03) // D2–D0: 128-step
	common128 := lnSLOT_COMMON | dec128
	inUse128 := lnSLOT_IN_USE | dec128
	if common128&lnSLOT_STA_MASK != lnSLOT_COMMON {
		t.Fatalf("COMMON|128-step %#x masked to %#x, want COMMON %#x",
			common128, common128&lnSLOT_STA_MASK, lnSLOT_COMMON)
	}
	if inUse128&lnSLOT_STA_MASK != lnSLOT_IN_USE {
		t.Fatalf("IN_USE|128-step %#x masked to %#x, want IN_USE %#x",
			inUse128, inUse128&lnSLOT_STA_MASK, lnSLOT_IN_USE)
	}
	if common128&lnSLOT_STA_MASK == lnSLOT_IN_USE {
		t.Fatal("COMMON|128-step must not look like IN_USE")
	}
}

func TestLnSlotToCommonPreservesDecoderBits(t *testing.T) {
	const dec128 = byte(0x03)
	got := lnSlotToCommon(lnSLOT_IN_USE | dec128)
	if got != (lnSLOT_COMMON | dec128) {
		t.Fatalf("lnSlotToCommon(IN_USE|128) = %#x, want %#x", got, lnSLOT_COMMON|dec128)
	}
	if lnSlotToCommon(0) != lnSLOT_COMMON {
		t.Fatalf("lnSlotToCommon(0) = %#x, want bare COMMON", lnSlotToCommon(0))
	}
	// Consist mid (D6|D3) must survive release.
	const consistMid = byte(0x40 | 0x08)
	got = lnSlotToCommon(lnSLOT_IN_USE | consistMid | dec128)
	if got != (lnSLOT_COMMON | consistMid | dec128) {
		t.Fatalf("consist bits lost: got %#x", got)
	}
}

func TestLnBuildMoveSlots(t *testing.T) {
	// NULL MOVE: src == dst
	pkt := lnBuildMoveSlots(5, 5)
	if len(pkt) != 4 {
		t.Fatalf("expected 4-byte packet, got %d: % X", len(pkt), pkt)
	}
	if pkt[0] != lnOPC_MOVE_SLOTS || pkt[1] != 5 || pkt[2] != 5 {
		t.Fatalf("null move: got % X", pkt)
	}
	if !lnChecksumOK(pkt) {
		t.Fatalf("null move checksum invalid: % X", pkt)
	}

	// Dispatch PUT: dst == 0
	put := lnBuildMoveSlots(7, 0)
	if put[1] != 7 || put[2] != 0 {
		t.Fatalf("dispatch PUT: got % X", put)
	}
	if !lnChecksumOK(put) {
		t.Fatalf("dispatch PUT checksum invalid: % X", put)
	}

	// Dispatch GET: src == 0, dst == 0
	get := lnBuildMoveSlots(0, 0)
	if get[1] != 0 || get[2] != 0 {
		t.Fatalf("dispatch GET: got % X", get)
	}
	if !lnChecksumOK(get) {
		t.Fatalf("dispatch GET checksum invalid: % X", get)
	}
}

func TestLnBuildSlotStat1(t *testing.T) {
	pkt := lnBuildSlotStat1(3, lnSLOT_COMMON)
	if len(pkt) != 4 {
		t.Fatalf("expected 4-byte packet, got %d: % X", len(pkt), pkt)
	}
	if pkt[0] != lnOPC_SLOT_STAT1 || pkt[1] != 3 || pkt[2] != lnSLOT_COMMON {
		t.Fatalf("slot stat1: got % X", pkt)
	}
	if !lnChecksumOK(pkt) {
		t.Fatalf("slot stat1 checksum invalid: % X", pkt)
	}
}

func TestDccPacketFunctionsOffBits(t *testing.T) {
	// F13..F20 group with only F15 on; others must be reported off.
	dcc, _ := dccFnGroupPacket(7, 15, 1<<15)
	addr, fns, ok := dccPacketFunctions(dcc)
	if !ok || addr != 7 {
		t.Fatalf("decode: ok=%v addr=%d", ok, addr)
	}
	if !fns[15] {
		t.Fatalf("F15 should be on")
	}
	if fns[14] || fns[16] {
		t.Fatalf("neighbours should be off: %v", fns)
	}
}
