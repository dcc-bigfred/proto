package commandstation

import "errors"

// EmergencyStopper is an OPTIONAL capability for drivers that can bypass the
// normal TX queue for an emergency stop so it is not delayed behind a full
// throttle queue.
type EmergencyStopper interface {
	EmergencyStop(addr LocoAddr, forward bool) error
}

// EmergencyStop sends wire speed 1 (DCC emergency stop) on the estop-priority
// channel, bypassing a full normal TX queue. When the slot was not already
// IN_USE on the command station, it is released after the stop so transient
// acquires (e.g. daemon boot-stop of an idle roster loco) do not consume a
// slot on the master.
//
// When allocatePhysicalSlots is enabled and another throttle already holds
// the slot IN_USE, the stop is still sent (safety) but BigFred does not
// adopt ownership or release the slot to COMMON.
func (l *LocoNet) EmergencyStop(addr LocoAddr, forward bool) error {
	// No-observe: an estop of a loco that BigFred does not lease must not
	// register a synthetic "external" slot lease (heldBefore keeps the slot).
	slot, heldBefore, err := l.acquireSlotWithHeldNoObserve(addr)
	if errors.Is(err, ErrSlotInUse) {
		// Slot number is returned even on ErrSlotInUse so we can stop without
		// a second LOCO_ADR round-trip and without claiming ownership.
		return l.sendEstopFrames(addr, slot, forward, false)
	}
	if err != nil {
		return err
	}
	if err := l.sendEstopFrames(addr, slot, forward, true); err != nil {
		return err
	}
	if heldBefore {
		return nil
	}
	return l.ReleaseSlot(addr)
}

// sendEstopFrames enqueues wire speed 1 (and DIRF when direction changes).
// When own is true, local speed/dir caches and acquired-at are updated as for
// a normal drive path. When false (external IN_USE stop), only the wire
// frames are sent — no ownership adoption.
func (l *LocoNet) sendEstopFrames(addr LocoAddr, slot byte, forward, own bool) error {
	fl := l.addrFnLock(addr)
	fl.Lock()
	defer fl.Unlock()

	if err := l.txEnqueue(lnBuildSetSpeed(slot, 1), lnTxPriorityEstop); err != nil {
		return err
	}
	if !own {
		return nil
	}
	l.setSpd(addr, 1)
	l.markAcquired(addr)

	dirf := l.getDirf(addr)
	want := dirf
	if forward {
		want |= 0x20
	} else {
		want &^= 0x20
	}
	if want != dirf {
		if err := l.txEnqueue(lnBuildSetDirF(slot, want), lnTxPriorityEstop); err != nil {
			return err
		}
		l.setDirf(addr, want)
	}
	return nil
}
