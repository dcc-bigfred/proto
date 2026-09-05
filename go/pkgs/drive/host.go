// Package drive is the protocol-agnostic backend a Z21 or WiThrottle server
// calls, and the push surface the consumer uses to fan loco state out to
// handsets. Pairing, roster policy, and Redis stay in the consumer.
package drive

// ClientID identifies one inbound handset (UDP address, or TCP conn + HU).
type ClientID string

// LocoState is the snapshot a server needs to answer GET_LOCO_INFO / M0A
// and to push NotifyLocoState.
type LocoState struct {
	Addr      uint16
	Speed     uint8
	Forward   bool
	Steps     uint8
	Functions uint32 // bitmask F0..F31
}

// DriveHost is implemented by the consumer (BigFred Router later; tests use
// a recorder).
type DriveHost interface {
	SetSpeed(client ClientID, addr uint16, speed uint8, forward bool, steps uint8) error
	SetFunction(client ClientID, addr uint16, fn uint8, on bool) error
	LocoState(addr uint16) (LocoState, error)
	SetTrackPower(client ClientID, on bool) error
	Release(client ClientID, addr uint16)
}

// FunctionModer is an optional DriveHost extension. When the host does not
// implement it, the WiThrottle server treats F2 as momentary and every other
// function as latching (JMRI default).
type FunctionModer interface {
	Momentary(addr uint16, fn uint8) bool
}

// AcquireGate is consulted on WiThrottle M+ before the default acquire reply.
// proceed=false skips Subscribe/LocoState; customReply is written as-is.
// If customReply contains an M+ line, the server still records the loco so
// later M A actions can be gated (sentinel pairing).
type AcquireGate interface {
	Acquire(client ClientID, addr uint16) (proceed bool, customReply []string)
}

// ActionGate is consulted on WiThrottle M A before SetSpeed/SetFunction.
// handled=true skips the default drive call for that address.
type ActionGate interface {
	Action(client ClientID, throttleID byte, locoKey string, addr uint16, prop string) (handled bool)
}

// ReleaseGate is consulted on WiThrottle M- before host.Release.
// GateRelease handled=true skips the default release line and host.Release.
// Named GateRelease so a DriveHost adapter can implement both interfaces.
type ReleaseGate interface {
	GateRelease(client ClientID, throttleID byte, locoKey string, addr uint16) (handled bool)
}

// TrackPowerGate is consulted on client PPA. handled=true skips SetTrackPower
// and the PPA broadcast (BigFred v1 ignores client track-power).
type TrackPowerGate interface {
	TrackPower(client ClientID, on bool) (handled bool)
}

// Subscriber is called after AcquireGate.Acquire(proceed=true), before LocoState.
type Subscriber interface {
	Subscribe(client ClientID, addr uint16) error
}

// SessionHooks observes WiThrottle TCP lifecycle. Servers type-assert the host.
type SessionHooks interface {
	OnConnect(client ClientID, deviceID string)
	OnActivity(client ClientID)
	OnQuit(client ClientID)
	OnDisconnect(client ClientID)
}

// NHook is consulted on WiThrottle N<name>. consumed=true means the consumer
// handled pairing and the server must not reply *<heartbeat>.
type NHook interface {
	OnN(client ClientID, name string) (consumed bool)
}
