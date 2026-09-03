// Package loconet implements LocoNet framing (checksums, packet length) and
// a TCP gateway that fans one upstream bus out to binary and ASCII listeners.
package loconet

// ChecksumOK reports whether the XOR of all bytes including the checksum is 0xFF.
func ChecksumOK(pkt []byte) bool {
	if len(pkt) < 2 {
		return false
	}
	var x byte
	for _, b := range pkt {
		x ^= b
	}
	return x == 0xFF
}

// AppendChecksum appends the Digitrax checksum byte.
func AppendChecksum(msg []byte) []byte {
	var x byte
	for _, b := range msg {
		x ^= b
	}
	return append(msg, x^0xFF)
}

// MsgLen is the on-wire length encoded in opcode bits 5–6.
func MsgLen(opcode byte, buf []byte) (int, bool) {
	switch (opcode >> 5) & 0x03 {
	case 0:
		return 2, true
	case 1:
		return 4, true
	case 2:
		return 6, true
	default:
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

// MaxFrame is the largest legal LocoNet frame (variable-length count byte).
const MaxFrame = 127

// StreamParser incrementally reconstructs packets from a byte stream.
type StreamParser struct {
	cur []byte
}

// PushByte feeds one byte. ok is true when a complete frame is returned.
func (p *StreamParser) PushByte(b byte) (pkt []byte, ok bool) {
	if len(p.cur) == 0 {
		if (b & 0x80) == 0 {
			return nil, false
		}
		p.cur = append(p.cur, b)
		return nil, false
	}
	if (b & 0x80) != 0 {
		p.cur = []byte{b}
		return nil, false
	}
	p.cur = append(p.cur, b)
	want, known := MsgLen(p.cur[0], p.cur)
	if !known || want == 0 || want > MaxFrame {
		p.cur = p.cur[:0]
		return nil, false
	}
	if len(p.cur) < want {
		if len(p.cur) > MaxFrame {
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

// Buffered is the partial frame held by the parser (empty when idle).
func (p *StreamParser) Buffered() []byte {
	return append([]byte(nil), p.cur...)
}
