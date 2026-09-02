package commandstation

// LocoObservation is a state change a driver observed on the command
// station, including changes authored by an EXTERNAL throttle (a
// physical handheld plugged into the same command station, not driven
// through BigFred).
//
// A driver may report a partial update: only fields whose Has* flag is
// set are meaningful. For example a LocoNet OPC_LOCO_SPD packet carries
// only speed, while OPC_LOCO_DIRF carries direction plus F0..F4. The
// consumer is expected to merge the partial update onto the last known
// snapshot for the address.
//
// Speed is reported in the same units the driver's GetSpeed returns, so
// callers can treat observed and polled speeds interchangeably.
type LocoObservation struct {
	Addr LocoAddr

	HasSpeed bool
	Speed    uint8

	HasForward bool
	Forward    bool

	// FunctionMask selects which function bits in FunctionBits are
	// meaningful for this observation (zero means no function info).
	// A driver SHOULD set every bit in the group it knows about (both
	// on and off) so the consumer can detect a function being turned
	// off, not only on.
	FunctionMask uint32
	FunctionBits uint32
}

// FnOn reports whether function fn is on in this observation. ok is false
// when fn is outside FunctionMask.
func (o LocoObservation) FnOn(fn int) (on bool, ok bool) {
	if fn < 0 || fn > 31 {
		return false, false
	}
	bit := uint32(1) << uint(fn)
	if o.FunctionMask&bit == 0 {
		return false, false
	}
	return o.FunctionBits&bit != 0, true
}

// mergeIn folds other into o in place: newer scalar fields win, function
// bits are unioned under the combined mask.
func (o *LocoObservation) mergeIn(other LocoObservation) {
	if other.HasSpeed {
		o.HasSpeed, o.Speed = true, other.Speed
	}
	if other.HasForward {
		o.HasForward, o.Forward = true, other.Forward
	}
	o.FunctionMask |= other.FunctionMask
	o.FunctionBits = (o.FunctionBits &^ other.FunctionMask) | (other.FunctionBits & other.FunctionMask)
}

// lnDirfFnObservation builds function mask/bits for F0..F4 from a DIRF byte.
func lnDirfFnObservation(dirf byte) (mask, bits uint32) {
	mask = 0x1F
	for fn := 0; fn <= 4; fn++ {
		if getFnFromDirf(dirf, fn) {
			bits |= 1 << uint(fn)
		}
	}
	return mask, bits
}

// lnSndFnObservation builds function mask/bits for F5..F8 from an SND byte.
func lnSndFnObservation(snd byte) (mask, bits uint32) {
	mask = 0x1E0
	for fn := 5; fn <= 8; fn++ {
		if getFnFromSnd(snd, fn) {
			bits |= 1 << uint(fn)
		}
	}
	return mask, bits
}

// lnSlotFnObservation builds function mask/bits for F0..F8 from slot data.
func lnSlotFnObservation(dirf, snd byte) (mask, bits uint32) {
	dm, db := lnDirfFnObservation(dirf)
	sm, sb := lnSndFnObservation(snd)
	return dm | sm, db | sb
}

// fnMapToObservation converts a sparse function map (as returned by
// dccPacketFunctions) into mask/bits without retaining the map.
func fnMapToObservation(fns map[int]bool) (mask, bits uint32) {
	for fn, on := range fns {
		if fn < 0 || fn > 31 {
			continue
		}
		bit := uint32(1) << uint(fn)
		mask |= bit
		if on {
			bits |= bit
		}
	}
	return mask, bits
}

// StateObserver is an OPTIONAL capability implemented by command-station
// drivers that can asynchronously report state changes seen on the bus.
//
// Two mechanisms make this possible depending on the hardware:
//
//   - A shared serial/TCP bus such as LocoNet, where every device sees
//     every speed/direction/function packet authored by any throttle.
//   - A station-level push/broadcast such as the Z21
//     LAN_SET_BROADCASTFLAGS + LAN_X_LOCO_INFO subscription.
//
// Callers MUST treat the capability as optional: type-assert for it and
// fall back to polling GetSpeed / ListFunctions when a driver does not
// implement it (see the dcc-bus state feed).
type StateObserver interface {
	// ObserveStates returns a channel that emits a LocoObservation for
	// every state change the driver sees on the bus. The driver stops
	// emitting after CleanUp. Implementations return the same channel
	// on repeated calls.
	ObserveStates() <-chan LocoObservation
}

// LocoInfoSubscriber is an OPTIONAL capability for StateObserver drivers
// that only push unsolicited state for locomotives they were explicitly
// told to watch.
//
// The Z21 is the motivating case: its broadcast for *every* modified loco
// (LAN_SET_BROADCASTFLAGS flag 0x00010000) only exists on FW ≥ 1.24. On
// older firmware the station pushes LAN_X_LOCO_INFO only for addresses
// the client subscribed to via LAN_X_GET_LOCO_INFO (flag 0x00000001).
// Without an explicit subscription, an external handset moving a loco the
// daemon never queried is invisible — exactly the symptom this fixes.
//
// Shared-bus drivers (LocoNet) see every packet regardless and need not
// implement this. Callers MUST treat it as optional (type-assert).
type LocoInfoSubscriber interface {
	// SubscribeLocoInfo asks the station to push unsolicited state for
	// addr until further notice. It is fire-and-forget: any immediate
	// reply is delivered through ObserveStates like any other change.
	// Re-subscribing an already-subscribed address is harmless.
	SubscribeLocoInfo(addr LocoAddr) error
}
