package commandstation

import (
	"errors"
	"fmt"
)

// LocoNet driver sentinel errors. Callers use errors.Is to distinguish them
// from wrapped transport or timeout failures.
var (
	// ErrSlotBusUnavailable is returned when the slot-acquire circuit breaker
	// is open after repeated command-station timeouts.
	ErrSlotBusUnavailable = errors.New("loconet: slot bus temporarily unavailable (circuit breaker open)")

	// ErrNoFreeSlot is returned when the command station rejects LOCO_ADR with
	// OPC_LONG_ACK code 0x00 (no free slot in the 120-slot table).
	ErrNoFreeSlot = errors.New("loconet: command station has no free slot")

	// ErrSlotInUse is returned when allocatePhysicalSlots is enabled and the
	// command station reports the loco already IN_USE by another throttle
	// (e.g. a physical FRED). BigFred must not piggyback or steal.
	ErrSlotInUse = errors.New("loconet: slot already in use by another throttle")

	// ErrBigFredSlotBudgetExceeded is returned when the SlotLeaser would exceed
	// max_loconet_slots before calling AcquireSlot on the command station.
	ErrBigFredSlotBudgetExceeded = errors.New("loconet: bigfred slot budget exceeded")

	// ErrSlotAcquireTimeout is returned when a slot request/response sequence
	// (LOCO_ADR, RQ_SL_DATA, NULL MOVE) times out waiting for a reply.
	ErrSlotAcquireTimeout = errors.New("loconet: timeout waiting for slot data")

	// ErrSpeedSuperseded is returned when a newer SetSpeed for the same address
	// overtook this call before its frame reached the bus. The newer call owns
	// downstream state; callers must not write Redis/UI for a superseded call.
	ErrSpeedSuperseded = errors.New("loconet: speed superseded by a newer SetSpeed")
)

// IsSlotAcquireError reports whether err indicates a stale or missing slot
// and warrants a forced slot revalidation before retrying a drive command.
// ErrSlotInUse is intentionally excluded: retrying cannot steal an external
// throttle's slot and must not be treated as a transient mapping failure.
func IsSlotAcquireError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSlotBusUnavailable) ||
		errors.Is(err, ErrNoFreeSlot) ||
		errors.Is(err, ErrSlotAcquireTimeout) {
		return true
	}
	return false
}

// errSlotAcquireTimeout wraps ErrSlotAcquireTimeout with address/slot context.
func errSlotAcquireTimeout(addr LocoAddr, slot byte) error {
	if slot != 0 {
		return fmt.Errorf("%w for slot %d", ErrSlotAcquireTimeout, slot)
	}
	return fmt.Errorf("%w for loco %d", ErrSlotAcquireTimeout, addr)
}
