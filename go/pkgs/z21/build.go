package z21

import "encoding/binary"

// BuildGetSerialNumber is LAN_GET_SERIAL_NUMBER (client → Z21).
func BuildGetSerialNumber() []byte {
	return BuildLAN(HeaderGetSerialNumber, nil)
}

// BuildSerialReply is the LAN_GET_SERIAL_NUMBER response.
func BuildSerialReply(serial uint32) []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, serial)
	return BuildLAN(HeaderGetSerialNumber, data)
}

// ParseSerialReply extracts the serial from a LAN_GET_SERIAL_NUMBER reply.
func ParseSerialReply(pkt []byte) (uint32, bool) {
	if !ValidFrame(pkt) || len(pkt) < 8 {
		return 0, false
	}
	_, header, ok := PacketHeader(pkt)
	if !ok || header != HeaderGetSerialNumber {
		return 0, false
	}
	return binary.LittleEndian.Uint32(pkt[4:8]), true
}

// BuildSetBroadcastFlags is LAN_SET_BROADCASTFLAGS (§2.16).
func BuildSetBroadcastFlags(flags uint32) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint16(buf[0:2], 0x0008)
	binary.LittleEndian.PutUint16(buf[2:4], HeaderSetBroadcastFlags)
	binary.LittleEndian.PutUint32(buf[4:8], flags)
	return buf
}

// BuildBroadcastFlagsReply is LAN_GET_BROADCASTFLAGS.
func BuildBroadcastFlagsReply(flags uint32) []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, flags)
	return BuildLAN(HeaderGetBroadcastFlags, data)
}

// BuildSetLocoFunctionGroup is LAN_X_SET_LOCO_FUNCTION_GROUP.
// bits is a mask whose LSB is the lowest function of the group (see ParseSetLocoFunctionGroup).
func BuildSetLocoFunctionGroup(addr uint16, db0 byte, bits uint32) []byte {
	msb, lsb := AddrBytes(addr)
	var raw byte
	switch db0 {
	case 0x20:
		if bits&1 != 0 {
			raw |= 0x10
		}
		for i := uint(0); i < 4; i++ {
			if bits&(1<<(i+1)) != 0 {
				raw |= 1 << i
			}
		}
	case 0x21, 0x22:
		raw = byte(bits & 0x0F)
	case 0x23, 0x28:
		raw = byte(bits)
	case 0x29:
		raw = byte(bits & 0x07)
	}
	return xbus([]byte{0xE4, db0, msb, lsb, raw})
}

// BuildHWInfoReply is LAN_GET_HWINFO.
func BuildHWInfoReply(hwType, fwBCD uint32) []byte {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], hwType)
	binary.LittleEndian.PutUint32(data[4:8], fwBCD)
	return BuildLAN(HeaderGetHWInfo, data)
}

// BuildGetLocoInfo is LAN_X_GET_LOCO_INFO (0xE3 0xF0).
func BuildGetLocoInfo(addr uint16) []byte {
	msb, lsb := AddrBytes(addr)
	return xbus([]byte{0xE3, 0xF0, msb, lsb})
}

// BuildSetLocoFunction is LAN_X_SET_LOCO_FUNCTION (0xE4 0xF8).
func BuildSetLocoFunction(addr uint16, fn int, on bool) []byte {
	msb, lsb := AddrBytes(addr)
	var typeBits byte
	if on {
		typeBits = 0x40
	}
	db3 := typeBits | byte(fn&0x3F)
	return xbus([]byte{0xE4, 0xF8, msb, lsb, db3})
}

// BuildSetLocoDrive is LAN_X_SET_LOCO_DRIVE (0xE4 0x1S).
// speedSteps: 0=14, 2=28, 3 or 4=128 (SET nibble 3).
func BuildSetLocoDrive(addr uint16, speed uint8, forward bool, speedSteps uint8) []byte {
	msb, lsb := AddrBytes(addr)
	s := speedSteps & 0x0F
	if s == 4 {
		s = 3
	}
	db0 := byte(0x10 | s)
	db3 := EncodeDriveDB3(speed, forward, s)
	return xbus([]byte{0xE4, db0, msb, lsb, db3})
}

// BuildTrackPower is LAN_X_SET_TRACK_POWER_ON/OFF.
func BuildTrackPower(on bool) []byte {
	db0 := byte(0x80)
	if on {
		db0 = 0x81
	}
	return xbus([]byte{0x21, db0})
}

// BuildPomRead is LAN_X_CV_POM_READ_BYTE.
func BuildPomRead(addr uint16, cvWire uint16) []byte {
	msb, lsb := AddrBytes(addr)
	db3 := byte(0xE4 | byte((cvWire>>8)&0x03))
	db4 := byte(cvWire & 0xFF)
	return xbus([]byte{0xE6, 0x30, msb, lsb, db3, db4, 0x00})
}

// BuildPomWriteByte is LAN_X_CV_POM_WRITE_BYTE.
func BuildPomWriteByte(addr uint16, cvWire uint16, value byte) []byte {
	msb, lsb := AddrBytes(addr)
	db3 := byte(0xEC | byte((cvWire>>8)&0x03))
	db4 := byte(cvWire & 0xFF)
	return xbus([]byte{0xE6, 0x30, msb, lsb, db3, db4, value})
}

// BuildProgRead is LAN_X_CV_READ (23 11).
func BuildProgRead(cvWire uint16) []byte {
	return xbus([]byte{0x23, 0x11, byte(cvWire >> 8), byte(cvWire & 0xFF)})
}

// BuildProgWrite is LAN_X_CV_WRITE (24 12).
func BuildProgWrite(cvWire uint16, value byte) []byte {
	return xbus([]byte{0x24, 0x12, byte(cvWire >> 8), byte(cvWire & 0xFF), value})
}

// BuildCvResult is LAN_X_CV_RESULT (§6.5). cv is 1-based (NMRA).
func BuildCvResult(cv uint16, value byte) []byte {
	w := cvWire(cv)
	return xbus([]byte{0x64, 0x14, byte(w >> 8), byte(w & 0xFF), value})
}

// BuildCvNack is LAN_X_CV_NACK (§6.4).
func BuildCvNack() []byte {
	return xbus([]byte{0x61, 0x13})
}

// BuildCvNackSC is LAN_X_CV_NACK_SC (§6.3).
func BuildCvNackSC() []byte {
	return xbus([]byte{0x61, 0x12})
}

// BuildBCStopped is LAN_X_BC_STOPPED.
func BuildBCStopped() []byte {
	return xbus([]byte{0x81, 0x00})
}

// BuildProgrammingMode is LAN_X_BC_PROGRAMMING_MODE (61 02).
func BuildProgrammingMode() []byte {
	return xbus([]byte{0x61, 0x02})
}

// BuildRMBusDataChanged is an empty LAN_RMBUS_DATACHANGED for group.
func BuildRMBusDataChanged(group byte) []byte {
	data := make([]byte, 11)
	data[0] = group
	return BuildLAN(HeaderRMBusDataChanged, data)
}

// BuildLocoModeReply is LAN_GET_LOCOMODE with mode 0 (DCC).
func BuildLocoModeReply(addr uint16) []byte {
	data := []byte{byte(addr >> 8), byte(addr), 0}
	return BuildLAN(HeaderGetLocoMode, data)
}

func cvWire(cv uint16) uint16 {
	if cv == 0 {
		return 0
	}
	return cv - 1
}

const (
	hwTypeZ21Black uint32 = 0x00000201
	firmwareBCD    uint32 = 0x00000124
)

func buildGetVersionReply() []byte {
	return xbus([]byte{0x63, 0x21, 0x36, 0x12})
}

func buildFirmwareVersionReply() []byte {
	return xbus([]byte{0xF3, 0x0A, 0x01, 0x24})
}

func buildStatusChangedReply() []byte {
	return xbus([]byte{0x62, 0x22, 0x00})
}

const (
	emuMainCurrentMA         int16  = 22
	emuFilteredMainCurrentMA int16  = 20
	emuTemperatureC          int16  = 38
	emuSupplyVoltageMV       uint16 = 15200
	emuVCCVoltageMV          uint16 = 12000
	emuCapabilities          byte   = 0x01 | 0x10 | 0x20 // DCC + loco + accessory
)

func defaultSystemStateData() []byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint16(data[0:2], uint16(emuMainCurrentMA))
	binary.LittleEndian.PutUint16(data[4:6], uint16(emuFilteredMainCurrentMA))
	binary.LittleEndian.PutUint16(data[6:8], uint16(emuTemperatureC))
	binary.LittleEndian.PutUint16(data[8:10], emuSupplyVoltageMV)
	binary.LittleEndian.PutUint16(data[10:12], emuVCCVoltageMV)
	data[15] = emuCapabilities
	return data
}

func buildSystemStateReply() []byte {
	return BuildLAN(HeaderSystemStateData, defaultSystemStateData())
}
