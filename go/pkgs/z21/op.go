package z21

import "github.com/dcc-bigfred/proto/go/pkgs/drive"

// DriveOp identifies a Z21 drive/control verb for DriveGate.
type DriveOp int

const (
	DriveSetSpeed DriveOp = iota
	DriveSetFunction
	DriveSetFunctionGroup
	DriveGetLocoInfo
	DriveLocoEStop
	DriveSetStop
	DrivePurge
	DriveTrackPower
)

// CVOp identifies a programming verb for CVGate.
type CVOp int

const (
	CVPomRead CVOp = iota
	CVPomWrite
	CVProgRead
	CVProgWrite
)

// DriveGate is consulted before the default SetSpeed/SetFunction/echo path.
// handled=true means the consumer sent any replies itself.
type DriveGate interface {
	Drive(client drive.ClientID, op DriveOp, addr uint16, pkt []byte) (handled bool)
}

// CVGate is consulted for POM and programming-track CV packets.
// If handled, reply (may be concatenated datagrams) is written as-is.
type CVGate interface {
	CV(client drive.ClientID, op CVOp, addr uint16, cvWire uint16, value uint8) (handled bool, reply []byte)
}

// SessionHooks observes Z21 UDP peer lifecycle.
type SessionHooks interface {
	OnActivity(client drive.ClientID)
	OnLogoff(client drive.ClientID)
	OnBroadcastFlags(client drive.ClientID, flags uint32)
}
