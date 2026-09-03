package z21

import "encoding/binary"

// ParseGetLocoInfo is LAN_X_GET_LOCO_INFO.
func ParseGetLocoInfo(pkt []byte) (addr uint16, ok bool) {
	if len(pkt) < 9 {
		return 0, false
	}
	_, header, okHdr := PacketHeader(pkt)
	if !okHdr || header != HeaderXBus || pkt[4] != 0xE3 || pkt[5] != 0xF0 {
		return 0, false
	}
	return ParseAddr(pkt, 6)
}

// ParseSetLocoDrive is LAN_X_SET_LOCO_DRIVE.
func ParseSetLocoDrive(pkt []byte) (addr uint16, speed uint8, forward bool, ok bool) {
	if len(pkt) < 10 {
		return 0, 0, false, false
	}
	_, header, okHdr := PacketHeader(pkt)
	if !okHdr || header != HeaderXBus || pkt[4] != 0xE4 || (pkt[5]&0xF0) != 0x10 {
		return 0, 0, false, false
	}
	addr, ok = ParseAddr(pkt, 6)
	if !ok {
		return 0, 0, false, false
	}
	speed, forward = DecodeDriveFromLocoInfo(pkt[5]&0x0F, pkt[8])
	// SET uses S=3 for 128; INFO decode expects KKK=4. Map nibble 3 → treat as 128.
	if s := pkt[5] & 0x0F; s == 3 {
		speed, forward = DecodeDriveFromLocoInfo(0x04, pkt[8])
	}
	return addr, speed, forward, true
}

// ParseSetLocoFunction is LAN_X_SET_LOCO_FUNCTION.
func ParseSetLocoFunction(pkt []byte) (addr uint16, fn int, on bool, toggle bool, ok bool) {
	if len(pkt) < 10 {
		return 0, 0, false, false, false
	}
	_, header, okHdr := PacketHeader(pkt)
	if !okHdr || header != HeaderXBus || pkt[4] != 0xE4 || pkt[5] != 0xF8 {
		return 0, 0, false, false, false
	}
	addr, ok = ParseAddr(pkt, 6)
	if !ok {
		return 0, 0, false, false, false
	}
	sw := pkt[8] >> 6
	fn = int(pkt[8] & 0x3F)
	switch sw {
	case 0:
		return addr, fn, false, false, true
	case 1:
		return addr, fn, true, false, true
	case 2:
		return addr, fn, false, true, true
	default:
		return 0, 0, false, false, false
	}
}

// ParseSetLocoFunctionGroup is LAN_X_SET_LOCO_FUNCTION_GROUP (0xE4, DB0 0x20…0x29).
// bits is a mask whose LSB is function lo (the lowest function in the group).
func ParseSetLocoFunctionGroup(pkt []byte) (addr uint16, lo, hi uint8, bits uint32, ok bool) {
	if len(pkt) < 10 {
		return 0, 0, 0, 0, false
	}
	_, header, okHdr := PacketHeader(pkt)
	if !okHdr || header != HeaderXBus || pkt[4] != 0xE4 {
		return 0, 0, 0, 0, false
	}
	addr, ok = ParseAddr(pkt, 6)
	if !ok {
		return 0, 0, 0, 0, false
	}
	raw := pkt[8]
	switch pkt[5] {
	case 0x20: // F0–F4: F0 is wire bit 4, F1–F4 bits 0–3
		lo, hi = 0, 4
		if raw&0x10 != 0 {
			bits |= 1 << 0
		}
		for i := uint(0); i < 4; i++ {
			if raw&(1<<i) != 0 {
				bits |= 1 << (i + 1)
			}
		}
	case 0x21: // F5–F8
		lo, hi = 5, 8
		bits = uint32(raw & 0x0F)
	case 0x22: // F9–F12
		lo, hi = 9, 12
		bits = uint32(raw & 0x0F)
	case 0x23: // F13–F20
		lo, hi = 13, 20
		bits = uint32(raw)
	case 0x28: // F21–F28
		lo, hi = 21, 28
		bits = uint32(raw)
	case 0x29: // F29–F31
		lo, hi = 29, 31
		bits = uint32(raw & 0x07)
	default:
		return 0, 0, 0, 0, false
	}
	return addr, lo, hi, bits, true
}

// ParseSetBroadcastFlags is LAN_SET_BROADCASTFLAGS (0x50).
func ParseSetBroadcastFlags(pkt []byte) (flags uint32, ok bool) {
	if !ValidFrame(pkt) || len(pkt) < 8 {
		return 0, false
	}
	_, header, okHdr := PacketHeader(pkt)
	if !okHdr || header != HeaderSetBroadcastFlags {
		return 0, false
	}
	return binary.LittleEndian.Uint32(pkt[4:8]), true
}

// ParseTrackPower is LAN_X_SET_TRACK_POWER_ON (0x21 0x81) / OFF (0x21 0x80).
func ParseTrackPower(pkt []byte) (on bool, ok bool) {
	if len(pkt) < 7 {
		return false, false
	}
	_, header, okHdr := PacketHeader(pkt)
	if !okHdr || header != HeaderXBus || pkt[4] != 0x21 {
		return false, false
	}
	switch pkt[5] {
	case 0x81:
		return true, true
	case 0x80:
		return false, true
	default:
		return false, false
	}
}

func handshakeReply(pkt []byte, serial uint32) ([]byte, bool) {
	_, header, ok := PacketHeader(pkt)
	if !ok {
		return nil, false
	}
	switch header {
	case HeaderGetSerialNumber:
		return BuildSerialReply(serial), true
	case HeaderGetHWInfo:
		return BuildHWInfoReply(hwTypeZ21Black, firmwareBCD), true
	case HeaderGetCode:
		return BuildLAN(HeaderGetCode, []byte{0x00}), true
	case HeaderSystemStateGetData:
		return buildSystemStateReply(), true
	case HeaderXBus:
		if len(pkt) < 7 {
			return nil, false
		}
		switch pkt[4] {
		case 0x21:
			switch pkt[5] {
			case 0x21:
				return buildGetVersionReply(), true
			case 0x24:
				return buildStatusChangedReply(), true
			}
		case 0xF1:
			if pkt[5] == 0x0A {
				return buildFirmwareVersionReply(), true
			}
		}
	}
	return nil, false
}
