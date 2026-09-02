package commandstation

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
)

// z21Metrics holds lock-free counters incremented on the Z21 hot path.
//
// It deliberately imports no telemetry library: the adapter only bumps atomic
// counters, while the dcc-bus layer periodically reads a snapshot and maps it
// onto OpenTelemetry instruments. This keeps OTel out of the driver entirely
// and keeps the transmit/receive path allocation-free and contention-free.
type z21Metrics struct {
	// Throughput.
	txPackets atomic.Uint64
	txBytes   atomic.Uint64
	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txByType  [256]atomic.Uint64
	rxByType  [256]atomic.Uint64

	// Saturation / backpressure.
	obsDropped  atomic.Uint64
	syncDropped atomic.Uint64

	// Reliability.
	txErrors     atomic.Uint64
	rxErrors     atomic.Uint64
	syncTimeouts atomic.Uint64
	cvNacks      atomic.Uint64
	cvNackSC     atomic.Uint64
}

func newZ21Metrics() *z21Metrics { return &z21Metrics{} }

func z21MsgType(pkt []byte) byte {
	if len(pkt) < 5 {
		return 0
	}
	hdr := binary.LittleEndian.Uint16(pkt[2:4])
	if hdr == 0x0040 {
		return pkt[4]
	}
	// System-level packets (e.g. LAN_SET_BROADCASTFLAGS header 0x0050) have no
	// X-BUS opcode; fold the LAN header low byte into the high bit so they do
	// not collide with X-BUS opcodes.
	return byte(0x80 | (hdr & 0x7F))
}

func (m *z21Metrics) countTx(pkt []byte, n int) {
	if m == nil {
		return
	}
	m.txPackets.Add(1)
	m.txBytes.Add(uint64(n))
	m.txByType[z21MsgType(pkt)].Add(1)
}

func (m *z21Metrics) countRx(pkt []byte, n int) {
	if m == nil {
		return
	}
	m.rxPackets.Add(1)
	m.rxBytes.Add(uint64(n))
	m.rxByType[z21MsgType(pkt)].Add(1)
}

func (m *z21Metrics) incr(c *atomic.Uint64) {
	if m == nil {
		return
	}
	c.Add(1)
}

// Z21MetricsSnapshot is an OpenTelemetry-free, point-in-time view of the Z21
// driver's counters. The dcc-bus telemetry layer maps it onto OTel instruments;
// the driver itself never references OTel. Cumulative fields only ever grow;
// gauge fields (*Queue*, FnCacheEntries) are instantaneous.
type Z21MetricsSnapshot struct {
	// Throughput (cumulative).
	TxPackets uint64
	RxPackets uint64
	TxBytes   uint64
	RxBytes   uint64
	TxByType  map[byte]uint64
	RxByType  map[byte]uint64

	// Saturation / backpressure (cumulative).
	ObsDropped  uint64
	SyncDropped uint64

	// Reliability (cumulative).
	TxErrors     uint64
	RxErrors     uint64
	SyncTimeouts uint64
	CvNacks      uint64
	CvNackSC     uint64

	// Gauges (point-in-time).
	FnCacheEntries int64
	ObsQueueLen    int64
	ObsQueueCap    int64
	SyncQueueLen   int64
	SyncQueueCap   int64
}

func (m *z21Metrics) snapshot() Z21MetricsSnapshot {
	s := Z21MetricsSnapshot{
		TxPackets:    m.txPackets.Load(),
		RxPackets:    m.rxPackets.Load(),
		TxBytes:      m.txBytes.Load(),
		RxBytes:      m.rxBytes.Load(),
		ObsDropped:   m.obsDropped.Load(),
		SyncDropped:  m.syncDropped.Load(),
		TxErrors:     m.txErrors.Load(),
		RxErrors:     m.rxErrors.Load(),
		SyncTimeouts: m.syncTimeouts.Load(),
		CvNacks:      m.cvNacks.Load(),
		CvNackSC:     m.cvNackSC.Load(),
		TxByType:     make(map[byte]uint64),
		RxByType:     make(map[byte]uint64),
	}
	for i := 0; i < 256; i++ {
		if v := m.txByType[i].Load(); v > 0 {
			s.TxByType[byte(i)] = v
		}
		if v := m.rxByType[i].Load(); v > 0 {
			s.RxByType[byte(i)] = v
		}
	}
	return s
}

// Z21MsgTypeName returns a stable, low-cardinality label for a Z21 message
// type key (see z21MsgType), falling back to hex for unknown values.
func Z21MsgTypeName(typ byte) string {
	switch typ {
	case 0x21:
		return "track_power"
	case 0x23:
		return "cv_prog_read"
	case 0x24:
		return "cv_prog_write"
	case 0xE3:
		return "get_loco_info"
	case 0xE4:
		return "set_loco"
	case 0xE6:
		return "cv_pom"
	case 0xEF:
		return "loco_info"
	case 0x61:
		return "status"
	case 0x64:
		return "cv_result"
	case 0xD0: // header 0x0050
		return "set_broadcast_flags"
	default:
		return fmt.Sprintf("0x%02X", typ)
	}
}
