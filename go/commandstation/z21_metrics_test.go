package commandstation

import (
	"encoding/binary"
	"testing"
)

func TestZ21MetricsSnapshotCountsTraffic(t *testing.T) {
	z := &Z21Roco{
		syncCh:  make(chan []byte, 1),
		obsCh:   make(chan LocoObservation, 1),
		metrics: newZ21Metrics(),
	}

	txPkt := z.buildSetLocoSpeed(3, 10, true, 3)
	z.metrics.countTx(txPkt, len(txPkt))

	rxPkt := make([]byte, 14)
	binary.LittleEndian.PutUint16(rxPkt[0:2], 14)
	binary.LittleEndian.PutUint16(rxPkt[2:4], 0x0040)
	rxPkt[4] = 0xEF
	z.metrics.countRx(rxPkt, len(rxPkt))

	s := z.Z21MetricsSnapshot()
	if s.TxPackets != 1 || s.TxBytes != uint64(len(txPkt)) {
		t.Fatalf("tx: packets=%d bytes=%d, want 1/%d", s.TxPackets, s.TxBytes, len(txPkt))
	}
	if s.RxPackets != 1 || s.RxBytes != 14 {
		t.Fatalf("rx: packets=%d bytes=%d, want 1/14", s.RxPackets, s.RxBytes)
	}
	if s.TxByType[0xE4] != 1 {
		t.Fatalf("TxByType[E4] = %d, want 1", s.TxByType[0xE4])
	}
	if s.RxByType[0xEF] != 1 {
		t.Fatalf("RxByType[EF] = %d, want 1", s.RxByType[0xEF])
	}
}

func TestZ21MetricsSnapshotDroppedAndGauges(t *testing.T) {
	z := &Z21Roco{
		syncCh:       make(chan []byte, 1),
		obsCh:        make(chan LocoObservation, 1),
		fnStateCache: map[LocoAddr]fnState{42: {}},
		metrics:      newZ21Metrics(),
	}

	z.syncCh <- []byte{1}
	z.emit(LocoObservation{Addr: 1})
	z.metrics.incr(&z.metrics.syncDropped)
	z.metrics.incr(&z.metrics.obsDropped)

	s := z.Z21MetricsSnapshot()
	if s.SyncDropped != 1 || s.ObsDropped != 1 {
		t.Fatalf("dropped sync=%d obs=%d, want 1/1", s.SyncDropped, s.ObsDropped)
	}
	if s.FnCacheEntries != 1 {
		t.Fatalf("FnCacheEntries = %d, want 1", s.FnCacheEntries)
	}
	if s.ObsQueueLen != 1 || s.SyncQueueLen != 1 {
		t.Fatalf("queue lens obs=%d sync=%d, want 1/1", s.ObsQueueLen, s.SyncQueueLen)
	}
}
