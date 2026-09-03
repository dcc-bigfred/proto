package commandstation

import (
	"fmt"
	"sync/atomic"
)

// lnMetrics holds lock-free counters incremented on the LocoNet hot path.
//
// It deliberately imports no telemetry library: the adapter only bumps atomic
// counters, while the dcc-bus layer periodically reads a snapshot and maps it
// onto OpenTelemetry instruments. This keeps OTel out of the driver entirely
// (separation of concerns) and keeps the transmit/receive path allocation-free
// and contention-free, so instrumentation never throttles the bus.
//
// Counters are cumulative and monotonic; they are never reset. Consumers that
// need rates derive them from successive snapshots (or let an OTel cumulative
// reader do it). Per-opcode counts live in fixed [128]-entry arrays indexed by
// opcode&0x7F — every LocoNet opcode has its high bit set, so this covers all
// of them with O(1), allocation-free bumps.
type lnMetrics struct {
	// Throughput.
	txFrames   atomic.Uint64
	txBytes    atomic.Uint64
	rxFrames   atomic.Uint64
	txByOpcode [128]atomic.Uint64
	rxByOpcode [128]atomic.Uint64

	// Saturation / backpressure.
	paceWaitNanos atomic.Uint64 // cumulative time blocked in pace() (bus saturation)
	txCoalesced   atomic.Uint64 // stale speed frames dropped before transmit
	obsDropped    atomic.Uint64 // observations dropped because obsCh was full
	syncDropped   atomic.Uint64 // request/response replies dropped because syncCh was full

	// Reliability (driver-level; transport-level live in the transport).
	txErrors       atomic.Uint64
	lackRejections atomic.Uint64 // OPC_LONG_ACK carrying a reject code (0x00)

	// Slot lifecycle (driver API calls).
	slotAcquires     atomic.Uint64
	slotAcquireFails atomic.Uint64
	slotRetries      atomic.Uint64
	slotReleases     atomic.Uint64
	slotDispatches   atomic.Uint64
	keepaliveRefresh atomic.Uint64

	// Command-station slot state transitions (any slot, bus-wide).
	csSlotOccupied atomic.Uint64 // slot promoted to IN_USE on the command station
	csSlotReleased atomic.Uint64 // slot left IN_USE (COMMON / IDLE / FREE)
}

func newLnMetrics() *lnMetrics { return &lnMetrics{} }

// countTx records one transmitted frame of n bytes with opcode op.
func (m *lnMetrics) countTx(op byte, n int) {
	if m == nil {
		return
	}
	m.txFrames.Add(1)
	m.txBytes.Add(uint64(n))
	m.txByOpcode[op&0x7f].Add(1)
}

// countRx records one received (checksum-valid) frame with opcode op.
func (m *lnMetrics) countRx(op byte) {
	if m == nil {
		return
	}
	m.rxFrames.Add(1)
	m.rxByOpcode[op&0x7f].Add(1)
}

func (m *lnMetrics) addPaceWait(nanos int64) {
	if m == nil || nanos <= 0 {
		return
	}
	m.paceWaitNanos.Add(uint64(nanos))
}

func (m *lnMetrics) incr(c *atomic.Uint64) {
	if m == nil {
		return
	}
	c.Add(1)
}

// lnTransportStatsSnapshot carries low-level transport counters surfaced for
// telemetry. Transports that track these implement lnStatsTransport; the rest
// contribute zeros.
type lnTransportStatsSnapshot struct {
	RxBytes       uint64
	BadChecksum   uint64
	Reconnects    uint64
	WriteTimeouts uint64
	WriteErrors   uint64
}

// lnStatsTransport is an optional interface implemented by transports that
// expose reliability counters (bad checksums, reconnects, write timeouts).
type lnStatsTransport interface {
	lnTransportStats() lnTransportStatsSnapshot
}

// LnMetricsSnapshot is an OpenTelemetry-free, point-in-time view of the LocoNet
// driver's counters. The dcc-bus telemetry layer maps it onto OTel instruments;
// the driver itself never references OTel. Cumulative fields only ever grow;
// gauge fields (SlotsActive, *Queue*) are instantaneous.
type LnMetricsSnapshot struct {
	// Throughput (cumulative).
	TxFrames   uint64
	RxFrames   uint64
	TxBytes    uint64
	RxBytes    uint64
	TxByOpcode map[byte]uint64
	RxByOpcode map[byte]uint64

	// Saturation / backpressure (cumulative).
	PaceWaitSeconds float64
	TxCoalesced     uint64
	ObsDropped      uint64
	SyncDropped     uint64

	// Reliability (cumulative).
	TxErrors       uint64
	BadChecksum    uint64
	Reconnects     uint64
	WriteTimeouts  uint64
	LackRejections uint64

	// Slot lifecycle (cumulative, driver API).
	SlotAcquires     uint64
	SlotAcquireFails uint64
	SlotRetries      uint64
	SlotReleases     uint64
	SlotDispatches   uint64
	KeepaliveRefresh uint64

	// Command-station slot state transitions (cumulative, bus-wide).
	CsSlotOccupied uint64
	CsSlotReleased uint64

	// Gauges (point-in-time).
	SlotsActive  int64
	RxQueueLen   int64
	RxQueueCap   int64
	ObsQueueLen  int64
	ObsQueueCap  int64
	SyncQueueLen int64
	SyncQueueCap int64
	TxQueueLen   int64
	TxQueueCap   int64
}

// snapshot copies the cumulative driver counters. Transport stats and gauges
// are folded in by LocoNet.MetricsSnapshot, which has access to the transport
// and channels.
func (m *lnMetrics) snapshot() LnMetricsSnapshot {
	s := LnMetricsSnapshot{
		TxFrames:         m.txFrames.Load(),
		RxFrames:         m.rxFrames.Load(),
		TxBytes:          m.txBytes.Load(),
		PaceWaitSeconds:  float64(m.paceWaitNanos.Load()) / 1e9,
		TxCoalesced:      m.txCoalesced.Load(),
		ObsDropped:       m.obsDropped.Load(),
		SyncDropped:      m.syncDropped.Load(),
		TxErrors:         m.txErrors.Load(),
		LackRejections:   m.lackRejections.Load(),
		SlotAcquires:     m.slotAcquires.Load(),
		SlotAcquireFails: m.slotAcquireFails.Load(),
		SlotRetries:      m.slotRetries.Load(),
		SlotReleases:     m.slotReleases.Load(),
		SlotDispatches:   m.slotDispatches.Load(),
		KeepaliveRefresh: m.keepaliveRefresh.Load(),
		CsSlotOccupied:   m.csSlotOccupied.Load(),
		CsSlotReleased:   m.csSlotReleased.Load(),
		TxByOpcode:       make(map[byte]uint64),
		RxByOpcode:       make(map[byte]uint64),
	}
	for i := 0; i < 128; i++ {
		// Reconstruct the full opcode: the high bit is always set on LocoNet
		// opcodes, so the stored index i maps back to opcode i|0x80.
		if v := m.txByOpcode[i].Load(); v > 0 {
			s.TxByOpcode[byte(i)|0x80] = v
		}
		if v := m.rxByOpcode[i].Load(); v > 0 {
			s.RxByOpcode[byte(i)|0x80] = v
		}
	}
	return s
}

// LnOpcodeName returns a stable, low-cardinality label for a LocoNet opcode,
// e.g. "loco_spd" for 0xA0, falling back to the hex value for unknown opcodes.
// Exported so the telemetry layer can label per-opcode metrics consistently.
func LnOpcodeName(op byte) string {
	switch op {
	case lnOPC_LOCO_ADR:
		return "loco_adr"
	case lnOPC_RQ_SL_DATA:
		return "rq_sl_data"
	case lnOPC_MOVE_SLOTS:
		return "move_slots"
	case lnOPC_SLOT_STAT1:
		return "slot_stat1"
	case lnOPC_LOCO_SPD:
		return "loco_spd"
	case lnOPC_LOCO_DIRF:
		return "loco_dirf"
	case lnOPC_LOCO_SND:
		return "loco_snd"
	case lnOPC_WR_SL_DATA:
		return "wr_sl_data"
	case lnOPC_SL_RD_DATA:
		return "sl_rd_data"
	case lnOPC_IMM_PACKET:
		return "imm_packet"
	case lnOPC_LONG_ACK:
		return "long_ack"
	case lnOPC_BUSY:
		return "busy"
	case lnOPC_GPOFF:
		return "gpoff"
	case lnOPC_GPON:
		return "gpon"
	case lnOPC_IDLE:
		return "idle"
	default:
		return fmt.Sprintf("0x%02X", op)
	}
}
