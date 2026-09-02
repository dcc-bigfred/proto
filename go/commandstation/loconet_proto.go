package commandstation

import (
	"encoding/hex"
	"fmt"
)

// LocoNet checksum:
// The XOR of all bytes including the checksum byte must equal 0xFF.
func lnChecksumOK(pkt []byte) bool {
	if len(pkt) < 2 {
		return false
	}
	var x byte
	for _, b := range pkt {
		x ^= b
	}
	return x == 0xFF
}

func lnAppendChecksum(msg []byte) []byte {
	var x byte
	for _, b := range msg {
		x ^= b
	}
	// want x ^ chk == 0xFF => chk == x ^ 0xFF
	return append(msg, x^0xFF)
}

func lnMsgLen(opcode byte, buf []byte) (int, bool) {
	// Length encoding is in bits 5..6:
	// 00 => 2 bytes, 01 => 4 bytes, 10 => 6 bytes, 11 => variable length, second byte is total length.
	switch (opcode >> 5) & 0x03 {
	case 0:
		return 2, true
	case 1:
		return 4, true
	case 2:
		return 6, true
	default:
		// variable
		if len(buf) < 2 {
			return 0, false
		}
		l := int(buf[1])
		if l < 2 {
			return 0, false
		}
		return l, true
	}
}

type lnPacket []byte

func (p lnPacket) String() string {
	if len(p) == 0 {
		return "<empty>"
	}
	return hex.EncodeToString([]byte(p))
}

// lnStreamParser incrementally reconstructs packets from a byte stream.
type lnStreamParser struct {
	cur []byte
}

// lnMaxFrameLen is the largest legal LocoNet frame (variable-length count byte).
const lnMaxFrameLen = 127

func (p *lnStreamParser) PushByte(b byte) (pkt []byte, ok bool) {
	// Message starts at byte with opcode bit7 = 1.
	if len(p.cur) == 0 {
		if (b & 0x80) == 0 {
			return nil, false
		}
		p.cur = append(p.cur, b)
		return nil, false
	}

	// Mid-frame resync: a new opcode byte abandons the partial frame.
	if (b & 0x80) != 0 {
		p.cur = []byte{b}
		return nil, false
	}

	p.cur = append(p.cur, b)

	want, known := lnMsgLen(p.cur[0], p.cur)
	if !known || want == 0 || want > lnMaxFrameLen {
		p.cur = p.cur[:0]
		return nil, false
	}
	if len(p.cur) < want {
		// Guard against garbage that never completes a declared-length frame.
		if len(p.cur) > lnMaxFrameLen {
			p.cur = p.cur[:0]
		}
		return nil, false
	}

	if len(p.cur) > want {
		p.cur = p.cur[:0]
		return nil, false
	}

	pkt = append([]byte{}, p.cur...)
	p.cur = p.cur[:0]
	return pkt, true
}

const (
	lnOPC_LOCO_ADR   = 0xBF
	lnOPC_RQ_SL_DATA = 0xBB
	lnOPC_MOVE_SLOTS = 0xBA
	lnOPC_SLOT_STAT1 = 0xB5
	lnOPC_LOCO_SPD   = 0xA0
	lnOPC_LOCO_DIRF  = 0xA1
	lnOPC_LOCO_SND   = 0xA2
	lnOPC_WR_SL_DATA = 0xEF
	lnOPC_SL_RD_DATA = 0xE7
	lnOPC_IMM_PACKET = 0xED
	lnOPC_LONG_ACK   = 0xB4
	lnOPC_BUSY       = 0x81
	lnOPC_GPOFF      = 0x82
	lnOPC_GPON       = 0x83
	lnOPC_IDLE       = 0x85
)

// Slot status values (STAT1 bits D5/D4 = SL_BUSY/SL_ACTIVE).
// Digitrax PE 1.0 / JMRI LnConstants: LOCOSTAT_MASK = 0x30.
// After OPC_LOCO_ADR the master typically returns COMMON; a NULL MOVE
// promotes the slot to IN_USE so BigFred becomes the authoritative throttle.
//
// Do NOT use bits D1/D0 — those encode decoder speed-step mode (e.g. 128-step
// is 0x03). Masking with 0x03 mis-reads COMMON|128-step (0x13) as IN_USE.
const (
	lnSLOT_FREE     = byte(0x00) // D5=0 D4=0: no locomotive; empty
	lnSLOT_COMMON   = byte(0x10) // D5=0 D4=1: loco known, no active throttle
	lnSLOT_IDLE     = byte(0x20) // D5=1 D4=0: purged/idle, not refreshed
	lnSLOT_IN_USE   = byte(0x30) // D5=1 D4=1: actively controlled by a throttle
	lnSLOT_STA_MASK = byte(0x30)
)

// lnSlotToCommon builds a STAT1 byte for OPC_SLOT_STAT1 release: occupancy
// becomes COMMON while decoder-type (D2–D0) and consist (D6/D3) bits from
// prev are preserved. Writing bare lnSLOT_COMMON (0x10) would zero the
// speed-step encoding and can change the master's DCC packet mode.
func lnSlotToCommon(prev byte) byte {
	return (prev &^ lnSLOT_STA_MASK) | lnSLOT_COMMON
}

// Immediate-packet and programming-track constants (see
// docs/bigfred/protos/loconet.md §11, §16, §19.2).
const (
	// lnImmRepeats is how many times the master re-sends a function
	// packet on the track (NMRA practice repeats function packets).
	lnImmRepeats = 2

	// Programming slot and PCMD values for service-mode direct byte
	// access. The low two bits (0x03) are part of the values observed
	// from real command stations / JMRI, not just the bare PE 1.0 bits.
	lnPRG_SLOT          = 0x7C
	lnPCMD_READ_DIRECT  = 0x2B // 0x03 | byte(0x20) | direct(0x08): read direct byte, service mode
	lnPCMD_WRITE_DIRECT = 0x6B // 0x43 | byte(0x20) | direct(0x08): write direct byte, service mode

	// PSTAT result bits in the programming-slot reply.
	lnPSTAT_USER_ABORTED = 0x08
	lnPSTAT_READ_FAIL    = 0x04
	lnPSTAT_WRITE_FAIL   = 0x02
	lnPSTAT_NO_DECODER   = 0x01
)

func lnBuildLocoAdr(addr LocoAddr) []byte {
	lo := byte(addr & 0x7F)
	hi := byte((addr >> 7) & 0x7F)
	// OPC_LOCO_ADR is a 4-byte message: BF <ADR_HI> <ADR_LO> <CHK> (the
	// opcode's length code is 4). The earlier form inserted a spurious
	// extra byte (BF 00 <lo> <hi> <chk>), producing a malformed 5-byte
	// frame that the command station rejected on its length/checksum and
	// never answered with the E7 slot read — so every slot allocation
	// (and thus SetSpeed / SendFn / GetSpeed / ListFunctions) timed out.
	return lnAppendChecksum([]byte{lnOPC_LOCO_ADR, hi, lo})
}

func lnBuildRqSlotData(slot byte) []byte {
	return lnAppendChecksum([]byte{lnOPC_RQ_SL_DATA, slot, 0x00})
}

func lnBuildSetSpeed(slot byte, speed byte) []byte {
	return lnAppendChecksum([]byte{lnOPC_LOCO_SPD, slot, speed})
}

func lnBuildSetDirF(slot byte, dirf byte) []byte {
	return lnAppendChecksum([]byte{lnOPC_LOCO_DIRF, slot, dirf})
}

func lnBuildSetSnd(slot byte, snd byte) []byte {
	return lnAppendChecksum([]byte{lnOPC_LOCO_SND, slot, snd})
}

func lnBuildGlobalPower(on bool) []byte {
	opc := byte(lnOPC_GPOFF)
	if on {
		opc = lnOPC_GPON
	}
	return lnAppendChecksum([]byte{opc})
}

type lnSlotData struct {
	Slot  byte
	Stat1 byte // STAT1 byte from the slot read; SL_STA = Stat1 & lnSLOT_STA_MASK
	Addr  LocoAddr
	Speed byte
	DirF  byte
	Snd   byte
}

func parseLnSlotData(pkt []byte) (lnSlotData, bool) {
	// Expect: E7 0E <slot> <stat1> <adr_lo> <spd> <dirf> <trk> <stat2> <adr_hi> <snd> <id1> <id2> <chk>
	if len(pkt) < 14 || pkt[0] != lnOPC_SL_RD_DATA {
		return lnSlotData{}, false
	}
	if pkt[1] != 0x0E {
		// not the classic slot read length; still allow parsing if long enough
	}
	if !lnChecksumOK(pkt) {
		return lnSlotData{}, false
	}
	slot := pkt[2]
	adrLo := pkt[4]
	adrHi := pkt[9]
	addr := LocoAddr(adrLo&0x7F) | (LocoAddr(adrHi&0x7F) << 7)
	return lnSlotData{
		Slot:  slot,
		Stat1: pkt[3],
		Addr:  addr,
		Speed: pkt[5],
		DirF:  pkt[6],
		Snd:   pkt[10],
	}, true
}

// lnBuildMoveSlots builds an OPC_MOVE_SLOTS packet.
//   - NULL MOVE (promote COMMON/IDLE → IN_USE): src == dst
//   - Dispatch PUT (hand slot to dispatch slot):  dst == 0, src == slot
//   - Dispatch GET (acquire dispatched slot):     src == 0, dst == 0
func lnBuildMoveSlots(src, dst byte) []byte {
	return lnAppendChecksum([]byte{lnOPC_MOVE_SLOTS, src & 0x7F, dst & 0x7F})
}

// lnBuildSlotStat1 builds an OPC_SLOT_STAT1 packet that directly writes
// the STAT1 byte of the given slot. This is fire-and-forget: the command
// station does not reply. Callers releasing ownership should pass
// lnSlotToCommon(cachedStat1) so decoder-type bits are preserved.
func lnBuildSlotStat1(slot, stat1 byte) []byte {
	return lnAppendChecksum([]byte{lnOPC_SLOT_STAT1, slot & 0x7F, stat1 & 0x7F})
}

// --- Extended functions (F9..F28) via immediate DCC packets ---

// dccAddrBytes returns the 1- or 2-byte NMRA address prefix for addr.
// Addresses below 128 use the short (7-bit) form; 128 and above use the
// long (14-bit) form. BigFred has no per-loco long/short flag, so this
// follows the usual address-range heuristic (see loconet.md §11.1).
func dccAddrBytes(addr LocoAddr) []byte {
	if addr >= 128 {
		return []byte{0xC0 | byte((addr>>8)&0x3F), byte(addr & 0xFF)}
	}
	return []byte{byte(addr & 0x7F)}
}

// dccFnGroupPacket builds the NMRA DCC function-group instruction (without
// the trailing XOR error byte) for the group that contains fn, taking the
// on/off state of every function in that group from the F0..F31 bitmask
// `bits`. Only the F9..F28 groups are produced. ok is false otherwise.
func dccFnGroupPacket(addr LocoAddr, fn int, bits uint32) (pkt []byte, ok bool) {
	pkt = dccAddrBytes(addr)
	switch {
	case fn >= 9 && fn <= 12: // 1010 F12 F11 F10 F9
		var m byte
		for i := 0; i < 4; i++ {
			if bits&(1<<uint(9+i)) != 0 {
				m |= 1 << uint(i)
			}
		}
		return append(pkt, 0xA0|m), true
	case fn >= 13 && fn <= 20: // 0xDE <F20..F13>
		var m byte
		for i := 0; i < 8; i++ {
			if bits&(1<<uint(13+i)) != 0 {
				m |= 1 << uint(i)
			}
		}
		return append(pkt, 0xDE, m), true
	case fn >= 21 && fn <= 28: // 0xDF <F28..F21>
		var m byte
		for i := 0; i < 8; i++ {
			if bits&(1<<uint(21+i)) != 0 {
				m |= 1 << uint(i)
			}
		}
		return append(pkt, 0xDF, m), true
	}
	return nil, false
}

// lnBuildImmPacket wraps an NMRA DCC packet (its bytes WITHOUT the XOR
// error byte) in an OPC_IMM_PACKET message asking the master to emit it
// on the main track `repeats` times. Up to 5 DCC bytes fit.
//
// The DHI byte carries the D7 (MSB) of each payload byte. The Digitrax
// PE 1.0 spec also fixes its top three bits to 0b001; JMRI omits that
// and real command stations accept either, so we follow JMRI and emit
// only the MSB bits (decoders/observers ignore the fixed bit anyway).
func lnBuildImmPacket(dcc []byte, repeats int) ([]byte, error) {
	if len(dcc) < 1 || len(dcc) > 5 {
		return nil, fmt.Errorf("imm packet: dcc length %d out of range (1..5)", len(dcc))
	}
	if repeats < 1 {
		repeats = 1
	}
	if repeats > 8 {
		repeats = 8
	}
	reps := byte((len(dcc)&0x07)<<4) | byte((repeats-1)&0x07)
	var dhi byte
	im := make([]byte, 5)
	for i, b := range dcc {
		if b&0x80 != 0 {
			dhi |= 1 << uint(i)
		}
		im[i] = b & 0x7F
	}
	msg := []byte{lnOPC_IMM_PACKET, 0x0B, 0x7F, reps, dhi, im[0], im[1], im[2], im[3], im[4]}
	return lnAppendChecksum(msg), nil
}

// decodeImmDccPacket extracts the DCC packet bytes (address + instruction,
// no XOR byte) from an OPC_IMM_PACKET message, or nil if pkt is not a
// well-formed immediate DCC packet.
func decodeImmDccPacket(pkt []byte) []byte {
	if len(pkt) < 11 || pkt[0] != lnOPC_IMM_PACKET || pkt[1] != 0x0B || pkt[2] != 0x7F {
		return nil
	}
	n := int((pkt[3] >> 4) & 0x07)
	if n < 1 || n > 5 || len(pkt) < 5+n {
		return nil
	}
	dhi := pkt[4]
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = pkt[5+i] & 0x7F
		if (dhi>>uint(i))&0x01 != 0 {
			out[i] |= 0x80
		}
	}
	return out
}

// dccPacketFunctions decodes the loco address and the F9..F28 function
// bits carried by a DCC function-group packet. ok is false for any packet
// that is not an F9..F28 function group. The returned map covers exactly
// the functions of the matched group (so a consumer can detect off too).
func dccPacketFunctions(dcc []byte) (addr LocoAddr, fns map[int]bool, ok bool) {
	if len(dcc) < 2 {
		return 0, nil, false
	}
	var instr int
	b0 := dcc[0]
	switch {
	case b0 >= 1 && b0 <= 127: // short address
		addr = LocoAddr(b0)
		instr = 1
	case b0 >= 0xC0 && b0 <= 0xE7: // long (14-bit) address
		if len(dcc) < 3 {
			return 0, nil, false
		}
		addr = (LocoAddr(b0&0x3F) << 8) | LocoAddr(dcc[1])
		instr = 2
	default:
		return 0, nil, false
	}
	if instr >= len(dcc) {
		return 0, nil, false
	}
	ib := dcc[instr]
	fns = make(map[int]bool, 8)
	switch {
	case ib&0xF0 == 0xA0: // F9..F12
		for i := 0; i < 4; i++ {
			fns[9+i] = ib&(1<<uint(i)) != 0
		}
	case ib == 0xDE: // F13..F20
		if instr+1 >= len(dcc) {
			return 0, nil, false
		}
		m := dcc[instr+1]
		for i := 0; i < 8; i++ {
			fns[13+i] = m&(1<<uint(i)) != 0
		}
	case ib == 0xDF: // F21..F28
		if instr+1 >= len(dcc) {
			return 0, nil, false
		}
		m := dcc[instr+1]
		for i := 0; i < 8; i++ {
			fns[21+i] = m&(1<<uint(i)) != 0
		}
	default:
		return 0, nil, false
	}
	return addr, fns, true
}

// --- Programming track (CV read/write) over OPC_WR_SL_DATA slot 0x7C ---

// lnBuildProgTask builds an OPC_WR_SL_DATA task for the programming slot.
// cv0 is the 0-based CV address (CV1 → 0); val is the byte to write (pass
// 0 for reads). Mirrors JMRI's progTaskStart for direct byte mode.
func lnBuildProgTask(pcmd byte, cv0 uint16, val byte) []byte {
	cvh := byte(((cv0&0x300)>>4)|((cv0&0x80)>>7)) | ((val & 0x80) >> 6)
	cvl := byte(cv0 & 0x7F)
	msg := []byte{
		lnOPC_WR_SL_DATA, 0x0E, lnPRG_SLOT,
		pcmd,       // PCMD
		0x00,       // PSTAT (0 on request)
		0x00,       // HOPSA (service mode: no loco address)
		0x00,       // LOPSA
		0x00,       // TRK
		cvh,        // CVH (CV bits 7..9 + data MSB)
		cvl,        // CVL (CV bits 0..6)
		val & 0x7F, // DATA7 (data bits 0..6)
		0x7F,       // ID1 (PC throttle id)
		0x7F,       // ID2
	}
	return lnAppendChecksum(msg)
}

type lnProgReply struct {
	PStat byte
	Value byte
}

// parseLnProgReply parses an OPC_SL_RD_DATA reply for the programming slot.
func parseLnProgReply(pkt []byte) (lnProgReply, bool) {
	if len(pkt) < 14 || pkt[0] != lnOPC_SL_RD_DATA || pkt[2] != lnPRG_SLOT {
		return lnProgReply{}, false
	}
	if !lnChecksumOK(pkt) {
		return lnProgReply{}, false
	}
	cvh := pkt[8]
	data7 := pkt[10]
	// Data MSB (D7) lives in CVH bit1.
	val := (data7 & 0x7F) | ((cvh & 0x02) << 6)
	return lnProgReply{PStat: pkt[4], Value: val}, true
}

func scaleToLnSpeed(speed uint8, speedSteps uint8) (byte, error) {
	// LocoNet slot speed semantics:
	// 0x00 stop, 0x01 e-stop, 0x02..0x7F = increasing speed.
	if speedSteps != 14 && speedSteps != 28 && speedSteps != 128 {
		return 0, fmt.Errorf("invalid speed steps: %d (must be 14, 28, or 128)", speedSteps)
	}
	if speed == 0 || speed == 1 {
		return byte(speed), nil
	}

	// Map user speed (2..max) to 2..127 linearly.
	var max uint8
	switch speedSteps {
	case 14:
		max = 15
	case 28:
		max = 28
	case 128:
		max = 127
	}
	if speed > max {
		speed = max
	}
	// scale 2..max -> 2..127
	in := int(speed - 2)
	inMax := int(max - 2)
	if inMax <= 0 {
		return 2, nil
	}
	out := 2 + (in*int(127-2))/inMax
	if out < 2 {
		out = 2
	}
	if out > 127 {
		out = 127
	}
	return byte(out), nil
}
