package z21

import (
	"sync"
	"sync/atomic"
)

const (
	dispatchShards   = 32
	dispatchShardBuf = 128
)

type task func()

// dispatcher routes datagrams to a fixed pool of workers keyed by ClientID.
// Per-client order is preserved; a slow host callback on one shard cannot
// stall the UDP read loop or handsets on other shards.
type dispatcher struct {
	shards []chan task
	wg     sync.WaitGroup
	closed atomic.Bool
}

func newDispatcher(shards, buf int) *dispatcher {
	if shards <= 0 {
		shards = 1
	}
	d := &dispatcher{shards: make([]chan task, shards)}
	for i := range d.shards {
		ch := make(chan task, buf)
		d.shards[i] = ch
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			for t := range ch {
				runTask(t)
			}
		}()
	}
	return d
}

// runTask contains a panic from one datagram's host callback so a single
// bad packet cannot take down the shard worker (and with it the process).
func runTask(t task) {
	if t == nil {
		return
	}
	defer func() { _ = recover() }()
	t()
}

func shardIndex(key string, shards int) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h % uint32(shards))
}

// dispatch queues t on the shard for key. When the shard is saturated (or
// the dispatcher is closed) the task runs inline on the caller, trading
// per-client ordering for not stalling the UDP read loop — same policy as
// BigFred v1.
func (d *dispatcher) dispatch(key string, t task) {
	if d == nil || d.closed.Load() {
		runTask(t)
		return
	}
	ch := d.shards[shardIndex(key, len(d.shards))]
	select {
	case ch <- t:
	default:
		runTask(t)
	}
}

func (d *dispatcher) close() {
	if d == nil {
		return
	}
	if !d.closed.CompareAndSwap(false, true) {
		d.wg.Wait()
		return
	}
	for _, ch := range d.shards {
		close(ch)
	}
	d.wg.Wait()
}
