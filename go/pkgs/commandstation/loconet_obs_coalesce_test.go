package commandstation

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestLocoObservationMergeIn(t *testing.T) {
	var o LocoObservation
	o.mergeIn(LocoObservation{Addr: 1, HasSpeed: true, Speed: 10})
	o.mergeIn(LocoObservation{Addr: 1, HasForward: true, Forward: true})
	o.mergeIn(LocoObservation{Addr: 1, FunctionMask: 0x3, FunctionBits: 0x2})
	if !o.HasSpeed || o.Speed != 10 {
		t.Fatalf("speed = %d has=%v", o.Speed, o.HasSpeed)
	}
	if !o.HasForward || !o.Forward {
		t.Fatalf("forward = %v has=%v", o.Forward, o.HasForward)
	}
	if o.FunctionMask != 0x3 || o.FunctionBits != 0x2 {
		t.Fatalf("fn mask=%#x bits=%#x", o.FunctionMask, o.FunctionBits)
	}
}

func TestObsCoalescerMergesBurstPerAddr(t *testing.T) {
	stop := make(chan struct{})
	out := make(chan LocoObservation, 8)
	var dropped atomic.Uint64
	c := newObsCoalescer(out, 20*time.Millisecond, stop, &dropped)
	t.Cleanup(func() { close(stop) })

	for i := 0; i < 50; i++ {
		c.submit(LocoObservation{Addr: 10, HasSpeed: true, Speed: byte(i)})
		c.submit(LocoObservation{Addr: 20, HasSpeed: true, Speed: byte(i + 100)})
	}
	time.Sleep(60 * time.Millisecond)

	got := make(map[LocoAddr]LocoObservation)
	for {
		select {
		case o := <-out:
			got[o.Addr] = o
		default:
			goto done
		}
	}
done:
	if len(got) != 2 {
		t.Fatalf("flushed %d addrs, want 2", len(got))
	}
	if got[10].Speed != 49 {
		t.Fatalf("addr 10 speed = %d, want 49", got[10].Speed)
	}
	if got[20].Speed != 149 {
		t.Fatalf("addr 20 speed = %d, want 149", got[20].Speed)
	}
	if dropped.Load() != 0 {
		t.Fatalf("dropped = %d, want 0", dropped.Load())
	}
}
