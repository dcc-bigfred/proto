package commandstation

import (
	"errors"
	"fmt"
	"time"
)

type LocoCV struct {
	LocoId LocoAddr
	Cv     CV
}

// CV is a par of CVx=y, where y is optional and can be ""
type CV struct {
	Num   CVNum
	Value int
}

func (cv *CV) Repr() string {
	return fmt.Sprintf("%d=%d", cv.Num, cv.Value)
}

func (cv *CV) Translate() uint16 {
	return uint16(cv.Num - 1)
}

// SlotManager is an optional interface implemented by LocoNet drivers that
// support the full slot lifecycle: releasing ownership, dispatching to
// physical throttles, and acquiring slots dispatched by physical throttles.
// Callers type-assert the Station value before use.
type SlotManager interface {
	// AcquireSlot makes the driver the authoritative server-side owner of the
	// slot for addr, querying the command station fresh and asserting IN_USE.
	// It reclaims a slot the master purged to COMMON or reassigned while the
	// loco was idle, so control survives a client leaving and returning.
	// Slots are owned per-locomotive, not per-session; the drive-permission
	// layer is enforced separately by the caller.
	//
	// When allocatePhysicalSlots is enabled (PE 1.0 exclusive mode), an
	// already-IN_USE slot not owned by BigFred returns ErrSlotInUse.
	// When disabled (legacy piggyback), an already-IN_USE slot is left
	// untouched and BigFred may still drive it by slot number.
	AcquireSlot(addr LocoAddr) error

	// ForceAcquireSlot revalidates addr's slot against the command station,
	// ignoring any debounce window. Use when a client subscribes or when a
	// drive command failed and the local slot mapping may be stale.
	ForceAcquireSlot(addr LocoAddr) error

	// ReleaseSlot marks the slot for addr as COMMON on the command station
	// (no active throttle owner) and removes it from the local cache.
	// The locomotive continues at its current speed; call SetSpeed first
	// if a controlled stop is needed.
	ReleaseSlot(addr LocoAddr) error

	// DispatchSlot moves the slot for addr into the LocoNet dispatch slot
	// (OPC_MOVE_SLOTS src=slot, dst=0). A physical FRED or other throttle
	// can then claim it via a dispatch GET. Stop the loco first.
	DispatchSlot(addr LocoAddr) error

	// AcquireDispatched claims the slot currently held in the LocoNet
	// dispatch slot (OPC_MOVE_SLOTS src=0, dst=0) and returns the loco
	// address it controls. Returns (0, nil) when the dispatch slot is empty.
	AcquireDispatched() (LocoAddr, error)
}

// BootSlotReconciler is an optional interface for LocoNet drivers that can
// scan the command-station slot table on daemon boot and release IN_USE
// roster slots left from an unclean shutdown.
type BootSlotReconciler interface {
	ReconcileBootSlots(roster map[LocoAddr]struct{}) error
}

// SlotReconciler is an optional interface for LocoNet drivers that can
// actively read the current IN_USE status of a locomotive's slot, so the
// leaser can reconcile stale leases against physical reality (dropping
// orphaned "external" and lost leases). Callers type-assert the Station value.
type SlotReconciler interface {
	// SlotStatus actively queries the command station for addr's slot and
	// reports whether it is IN_USE. known is false when the driver has no
	// slot mapping for addr (nothing to reconcile). On a bus error err is
	// non-nil; callers MUST treat err or !known as "do not release" so a
	// transient failure never yanks a slot.
	SlotStatus(addr LocoAddr) (inUse bool, known bool, err error)
}

// SlotObserver receives slot lifecycle events from the driver. The driver
// emits OnSlotInUse only after BigFred-initiated acquire paths (SetSpeed,
// SendFn, AcquireSlot via acquireSlotWithHeld). Passive bus IN_USE transitions
// from external throttles are not reported here (they are tracked in CS slot
// metrics only). OnSlotReleased fires when BigFred releases a slot or when a
// mapped slot index transitions away from IN_USE on the bus.
// Implementations MUST be safe to call from the driver's hot path and MUST
// NOT block (the driver calls them synchronously under its own locks).
type SlotObserver interface {
	// OnSlotInUse is called after BigFred successfully acquires or revalidates
	// a slot for addr (driver-initiated, not passive bus observation).
	OnSlotInUse(addr LocoAddr)
	// OnSlotReleased is called after a slot for addr is no longer IN_USE
	// (we released it to COMMON, or the master purged/reassigned it, or an
	// external throttle released it and the bus reported COMMON).
	OnSlotReleased(addr LocoAddr)
}

// SlotObservable is implemented by drivers that can feed a SlotObserver.
// Callers type-assert the Station value and attach an observer before the
// bus read loop starts emitting events.
type SlotObservable interface {
	SetSlotObserver(obs SlotObserver)
}

// PhysicalSlotAllocator is implemented by LocoNet drivers that can switch
// between PE 1.0 exclusive slot allocation (like a physical FRED) and legacy
// piggyback on already-IN_USE slots. Callers type-assert after Open.
type PhysicalSlotAllocator interface {
	SetAllocatePhysicalSlots(enabled bool)
}

// SlotStealer is an optional LocoNet capability for an explicit user-confirmed
// takeover of a slot already IN_USE by another throttle (e.g. physical FRED).
// Normal AcquireSlot still refuses foreign IN_USE when allocatePhysicalSlots
// is enabled; StealSlot is the only path that releases then claims.
type SlotStealer interface {
	StealSlot(addr LocoAddr) error
}

// MetricsSource is an optional interface implemented by LocoNet drivers that
// expose a point-in-time snapshot of low-level counters for telemetry.
// Implementations import no telemetry library and only bump atomic counters
// on the hot path; the dcc-bus layer reads the snapshot and maps it onto
// OpenTelemetry instruments. Callers type-assert the Station value before use.
type MetricsSource interface {
	// MetricsSnapshot returns the current cumulative counters and instantaneous
	// gauges. It is safe to call concurrently with bus traffic.
	MetricsSnapshot() LnMetricsSnapshot
}

// Z21MetricsSource is an optional interface implemented by Z21 drivers that
// expose a point-in-time snapshot of low-level counters for telemetry.
type Z21MetricsSource interface {
	// Z21MetricsSnapshot returns the current cumulative counters and
	// instantaneous gauges. It is safe to call concurrently with bus traffic.
	Z21MetricsSnapshot() Z21MetricsSnapshot
}

// Station is the synchronous request/response surface every driver
// implements. Drivers that can additionally report state changes seen
// on the bus (including external throttles) also implement the optional
// StateObserver interface (see observer.go); callers type-assert for it
// and fall back to polling GetSpeed / ListFunctions when it is absent.
type Station interface {
	// WriteCV sends a write request to the command station to write CV of specific value for a given locomotive
	WriteCV(mode Mode, lcv LocoCV, options ...Option) error
	ReadCV(mode Mode, lcv LocoCV, options ...Option) (int, error)
	SendFn(mode Mode, addr LocoAddr, num FuncNum, toggle bool) error
	// ListFunctions returns a list of function numbers that are currently active (on) for the given locomotive
	ListFunctions(addr LocoAddr) ([]int, error)
	// SetSpeed sets the speed and direction of a locomotive
	SetSpeed(addr LocoAddr, speed uint8, forward bool, speedSteps uint8) error
	// GetSpeed retrieves the current speed and direction of a locomotive
	GetSpeed(addr LocoAddr) (speed uint8, forward bool, err error)
	CleanUp() error
}

// TrackPowerController is implemented by stations that can switch main-track
// power (LocoNet GPON/GPOFF, Z21 LAN_X_SET_TRACK_POWER_*).
type TrackPowerController interface {
	SetTrackPower(on bool) error
}

// ErrTrackPowerUnsupported is returned when the station cannot switch track power.
var ErrTrackPowerUnsupported = errors.New("commandstation: track power not supported")

// CV number
type CVNum uint16

// LocoAddr represents locomotive address
type LocoAddr uint16

// Function number
type FuncNum int

// Mode could be PoM or programming track. Depending on what's supported by your command station
type Mode string

const (
	MainTrackMode        Mode = "pom"
	ProgrammingTrackMode Mode = "prog"
)

// internal key for function-group cache
type fnStateKey struct {
	addr   LocoAddr
	fnType byte
}

//
// Contextual options
//

type ctxOptions func(*RequestContext) error

// Option configures per-request driver behaviour such as timeout or retries.
type Option = ctxOptions

type RequestContext struct {
	timeout time.Duration
	verify  bool
	retries uint8
	settle  time.Duration
}

func Timeout(timeout time.Duration) func(*RequestContext) error {
	return func(ctx *RequestContext) error {
		ctx.timeout = timeout
		return nil
	}
}

func Retries(retries uint8) func(*RequestContext) error {
	return func(ctx *RequestContext) error {
		ctx.retries = retries
		return nil
	}
}

func Verify(shouldVerify bool) func(*RequestContext) error {
	return func(ctx *RequestContext) error {
		ctx.verify = shouldVerify
		return nil
	}
}

func applyMethodsToCtx(ctx *RequestContext, options []ctxOptions) {
	for _, option := range options {
		option(ctx)
	}
}

// --- End of contextual options ---
