package z21

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
	case HeaderGetBroadcastFlags:
		return BuildLAN(HeaderGetBroadcastFlags, make([]byte, 4)), true
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
