package z21

import "github.com/dcc-bigfred/proto/go/drive"

// EncodeDriveDB3 builds DB3 (RVVVVVVV) for LAN_X_SET_LOCO_DRIVE (§4.2).
// speedSteps is the proto nibble S: 0=14, 2=28, 3=128.
// UI speed: 0=stop, 1=e-stop, 2+ = drive.
func EncodeDriveDB3(speed uint8, forward bool, speedSteps uint8) byte {
	var db3 byte
	if forward {
		db3 = 0x80
	}
	switch speed {
	case 0:
		return db3
	case 1:
		return db3 | 0x01
	}
	switch speedSteps {
	case 0:
		if speed > 15 {
			speed = 15
		}
		db3 |= speed & 0x0F
	case 2:
		if speed > 28 {
			speed = 28
		}
		speedBits := byte((speed + 3) / 2)
		speedBit5 := byte((speed + 3) % 2)
		db3 |= (speedBit5 << 4) | (speedBits & 0x0F)
	default:
		if speed > 127 {
			speed = 127
		}
		db3 |= speed & 0x7F
	}
	return db3
}

// DecodeDriveFromLocoInfo decodes DB2/DB3 from LAN_X_LOCO_INFO (§4.4).
func DecodeDriveFromLocoInfo(db2, db3 byte) (speed uint8, forward bool) {
	forward = (db3 & 0x80) != 0
	v := db3 & 0x7F
	switch db2 & 0x07 {
	case 0:
		return v & 0x0F, forward
	case 2:
		speedBits := v & 0x0F
		speedBit5 := (v >> 4) & 0x01
		raw := int(speedBits)*2 + int(speedBit5)
		switch {
		case raw <= 1:
			return 0, forward
		case raw <= 3:
			return 1, forward
		default:
			return uint8(raw - 3), forward
		}
	default:
		return v, forward
	}
}

// LocoInfo is a decoded LAN_X_LOCO_INFO payload.
type LocoInfo struct {
	Addr      uint16
	Speed     uint8
	Forward   bool
	Functions uint32
	DB2       byte
}

// ParseLocoInfo decodes a complete LAN_X_LOCO_INFO packet (0xEF).
func ParseLocoInfo(pkt []byte) (LocoInfo, bool) {
	if len(pkt) < 10 {
		return LocoInfo{}, false
	}
	dataLen, header, ok := PacketHeader(pkt)
	if !ok || header != HeaderXBus || int(dataLen) != len(pkt) || pkt[4] != 0xEF {
		return LocoInfo{}, false
	}
	addr, ok := ParseAddr(pkt, 5)
	if !ok {
		return LocoInfo{}, false
	}
	speed, forward := DecodeDriveFromLocoInfo(pkt[7], pkt[8])
	info := LocoInfo{Addr: addr, Speed: speed, Forward: forward, DB2: pkt[7]}
	if len(pkt) > 9 {
		info.Functions = decodeFunctionBytes(pkt[9:])
	}
	return info, true
}

func decodeFunctionBytes(db []byte) uint32 {
	var f uint32
	if len(db) > 0 {
		b0 := db[0]
		if b0&0x10 != 0 {
			f |= 1 << 0
		}
		for i := 0; i < 4; i++ {
			if b0&(1<<uint(i)) != 0 {
				f |= 1 << uint(i+1)
			}
		}
	}
	if len(db) > 1 {
		for i := 0; i < 8; i++ {
			if db[1]&(1<<uint(i)) != 0 {
				f |= 1 << uint(i+5)
			}
		}
	}
	if len(db) > 2 {
		for i := 0; i < 8; i++ {
			if db[2]&(1<<uint(i)) != 0 {
				f |= 1 << uint(i+13)
			}
		}
	}
	if len(db) > 3 {
		for i := 0; i < 8; i++ {
			if db[3]&(1<<uint(i)) != 0 {
				f |= 1 << uint(i+21)
			}
		}
	}
	if len(db) > 4 {
		for i := 0; i < 3; i++ {
			if db[4]&(1<<uint(i)) != 0 {
				f |= 1 << uint(i+29)
			}
		}
	}
	return f
}

func encodeFunctionBytes(fn uint32) (b0, b1, b2, b3, b4 byte) {
	if fn&(1<<0) != 0 {
		b0 |= 0x10
	}
	for i := 1; i <= 4; i++ {
		if fn&(1<<uint(i)) != 0 {
			b0 |= 1 << uint(i-1)
		}
	}
	for i := 5; i <= 12; i++ {
		if fn&(1<<uint(i)) != 0 {
			b1 |= 1 << uint(i-5)
		}
	}
	for i := 13; i <= 20; i++ {
		if fn&(1<<uint(i)) != 0 {
			b2 |= 1 << uint(i-13)
		}
	}
	for i := 21; i <= 28; i++ {
		if fn&(1<<uint(i)) != 0 {
			b3 |= 1 << uint(i-21)
		}
	}
	for i := 29; i <= 31; i++ {
		if fn&(1<<uint(i)) != 0 {
			b4 |= 1 << uint(i-29)
		}
	}
	return
}

func locoInfoDB2(steps uint8) byte {
	switch steps {
	case 14, 0:
		return 0
	case 28, 2:
		return 2
	default:
		return 4
	}
}

func driveProtoNibble(steps uint8) byte {
	switch steps {
	case 14, 0:
		return 0
	case 28, 2:
		return 2
	default:
		return 3 // SET_DRIVE uses 3; INFO reports KKK=4
	}
}

// BuildLocoInfo builds LAN_X_LOCO_INFO from a DriveHost snapshot.
func BuildLocoInfo(st drive.LocoState) []byte {
	steps := st.Steps
	if steps == 0 {
		steps = 128
	}
	db2 := locoInfoDB2(steps)
	db3 := EncodeDriveDB3(st.Speed, st.Forward, driveProtoNibble(steps))
	db4, db5, db6, db7, db8 := encodeFunctionBytes(st.Functions)
	msb, lsb := AddrBytes(st.Addr)
	x := []byte{0xEF, msb, lsb, db2, db3, db4, db5, db6, db7, db8}
	return xbus(x)
}
