// Package z21 implements the Z21 LAN UDP protocol: frame encode/decode,
// locomotive drive commands, CV/POM programming, decoder address helpers,
// and a loopback Listen server for tests.
package z21

import "encoding/binary"

// DefaultPort is the Z21 LAN UDP port (§1.1).
const DefaultPort uint16 = 21105

// LAN headers (little-endian uint16 at bytes 2–3).
const (
	HeaderGetSerialNumber    uint16 = 0x0010
	HeaderLogoff             uint16 = 0x0030
	HeaderXBus               uint16 = 0x0040
	HeaderSetBroadcastFlags  uint16 = 0x0050
	HeaderGetBroadcastFlags  uint16 = 0x0051
	HeaderGetHWInfo          uint16 = 0x001A
	HeaderGetCode            uint16 = 0x0018
	HeaderSystemStateData    uint16 = 0x0084
	HeaderSystemStateGetData uint16 = 0x0085
	HeaderRMBusGetData       uint16 = 0x0081
	HeaderRMBusDataChanged   uint16 = 0x0080
	HeaderGetLocoMode        uint16 = 0x0060
	HeaderLocoNetFromLAN     uint16 = 0x00A2
	HeaderLanKeepalive       uint16 = 0x0035
	HeaderLanSessionProbe    uint16 = 0x0036
)

// Broadcast flags (§2.16).
const (
	BcDrivingSwitching uint32 = 0x00000001
	BcAllLocos         uint32 = 0x00010000
	BcSystemState      uint32 = 0x00000100
)

// XOR of all bytes. X-Bus trailing checksum is XOR so the full X payload
// (including checksum) sums to 0.
func XOR(b []byte) byte {
	var x byte
	for _, v := range b {
		x ^= v
	}
	return x
}

// PacketHeader returns DataLen and Header from a LAN dataset.
func PacketHeader(pkt []byte) (dataLen, header uint16, ok bool) {
	if len(pkt) < 4 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint16(pkt[0:2]),
		binary.LittleEndian.Uint16(pkt[2:4]),
		true
}

// ValidFrame reports whether pkt is one well-formed Z21 dataset.
func ValidFrame(pkt []byte) bool {
	if len(pkt) < 4 {
		return false
	}
	dataLen, _, ok := PacketHeader(pkt)
	return ok && int(dataLen) == len(pkt)
}

// SplitDatagram splits a UDP payload into length-prefixed Z21 datasets (§1.3).
func SplitDatagram(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 4 {
		l := int(binary.LittleEndian.Uint16(b[0:2]))
		if l < 4 || l > len(b) {
			break
		}
		out = append(out, b[:l])
		b = b[l:]
	}
	return out
}

// BuildLAN builds DataLen + Header + data.
func BuildLAN(header uint16, data []byte) []byte {
	out := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint16(out[0:2], uint16(4+len(data)))
	binary.LittleEndian.PutUint16(out[2:4], header)
	copy(out[4:], data)
	return out
}

// BuildXBus wraps an X-Bus payload (already including XOR) in a LAN_X frame.
func BuildXBus(x []byte) []byte {
	return BuildLAN(HeaderXBus, x)
}

func xbus(payload []byte) []byte {
	return BuildXBus(append(payload, XOR(payload)))
}

// AddrBytes encodes a DCC address into Adr_MSB/Adr_LSB (§4).
func AddrBytes(addr uint16) (msb, lsb byte) {
	msb = byte((addr >> 8) & 0x3F)
	if addr >= 128 {
		msb |= 0xC0
	}
	return msb, byte(addr & 0xFF)
}

// ParseAddr reads a DCC address from pkt[offset:].
func ParseAddr(pkt []byte, offset int) (uint16, bool) {
	if len(pkt) < offset+2 {
		return 0, false
	}
	return uint16(pkt[offset]&0x3F)<<8 | uint16(pkt[offset+1]), true
}
