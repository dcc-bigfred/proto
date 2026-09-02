package commandstation

import (
	"errors"

	"github.com/sirupsen/logrus"

	"github.com/dcc-bigfred/proto/go/z21"
)

//
// Context: This file is containing methods to communicate with a DCC device using a Z21 protocol
//

// Read: LAN_X_CV_POM_READ_BYTE (E6 30 … option 0xE4)
func (z *Z21Roco) buildPomReadPacket(lcv LocoCV) []byte {
	return z21.BuildPomRead(uint16(lcv.LocoId), lcv.Cv.Translate())
}

// Write BYTE: LAN_X_CV_POM_WRITE_BYTE (E6 30 … option 0xEC)
func (z *Z21Roco) buildPomWriteByte(lcv LocoCV) []byte {
	return z21.BuildPomWriteByte(uint16(lcv.LocoId), lcv.Cv.Translate(), byte(lcv.Cv.Value))
}

// ===== PROG (Programming Track / Direct Mode) =====
// Read: LAN_X_CV_READ (23 11)
func (z *Z21Roco) buildProgReadPacket(cv CV) []byte {
	return z21.BuildProgRead(cv.Translate())
}

// Write: LAN_X_CV_WRITE (24 12)
func (z *Z21Roco) buildProgWritePacket(lcv LocoCV) []byte {
	return z21.BuildProgWrite(lcv.Cv.Translate(), byte(lcv.Cv.Value))
}

// Track power ON (get back from programming mode)
func (z *Z21Roco) buildTrackPowerOn() []byte {
	return z.buildTrackPower(true)
}

func (z *Z21Roco) buildTrackPowerOff() []byte {
	return z.buildTrackPower(false)
}

func (z *Z21Roco) buildTrackPower(on bool) []byte {
	return z21.BuildTrackPower(on)
}

// SetTrackPower implements TrackPowerController via LAN_X_SET_TRACK_POWER_*.
func (z *Z21Roco) SetTrackPower(on bool) error {
	if z == nil || z.currentConn() == nil {
		return ErrTrackPowerUnsupported
	}
	_, err := z.write(z.buildTrackPower(on))
	return err
}

// buildSetBroadcastFlags builds LAN_SET_BROADCASTFLAGS (§2.16). This is
// a LAN-level command (header 0x0050), not an X-Bus frame, so it has no
// XOR byte: DataLen(2) + Header(2) + 4-byte little-endian flags.
func buildSetBroadcastFlags(flags uint32) []byte {
	return z21.BuildSetBroadcastFlags(flags)
}

// buildGetLocoInfo builds LAN_X_GET_LOCO_INFO command (0xE3 0xF0)
func (z *Z21Roco) buildGetLocoInfo(addr LocoAddr) []byte {
	return z21.BuildGetLocoInfo(uint16(addr))
}

// buildSetLocoFunction builds LAN_X_SET_LOCO_FUNCTION command (0xE4 0xF8)
func (z *Z21Roco) buildSetLocoFunction(addr LocoAddr, fnNum int, on bool) []byte {
	return z21.BuildSetLocoFunction(uint16(addr), fnNum, on)
}

// buildSetLocoSpeed builds LAN_X_SET_LOCO_DRIVE command (0xE4 0x1S)
// speedSteps: 0=14 steps, 2=28 steps, 3=128 steps (SET nibble; INFO reports KKK=4)
// speed: 0=stop, 1=emergency stop, 2-127 (for 128 steps) actual speed
// forward: true for forward direction, false for reverse
func (z *Z21Roco) buildSetLocoSpeed(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) []byte {
	return z21.BuildSetLocoDrive(uint16(addr), speed, forward, speedSteps)
}

func (z *Z21Roco) write(b []byte) (n int, err error) {
	logrus.Debugf("write: % X", b)
	conn := z.currentConn()
	if conn == nil {
		z.metrics.incr(&z.metrics.txErrors)
		return 0, errors.New("z21: not connected")
	}
	n, err = conn.Write(b)
	if err != nil {
		z.metrics.incr(&z.metrics.txErrors)
		logrus.WithError(err).Warn("z21 command station: UDP write failed")
		go z.doReconnect()
		return n, err
	}
	for _, pkt := range splitZ21Datagram(b[:n]) {
		z.metrics.countTx(pkt, len(pkt))
	}
	return n, err
}
