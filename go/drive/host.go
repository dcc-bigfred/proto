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
