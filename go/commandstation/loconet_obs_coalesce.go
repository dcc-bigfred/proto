package commandstation

import (
	"sync"
	"sync/atomic"
	"time"
)

// lnObsCoalesceTick is how often pending observations are flushed to obsCh.
const lnObsCoalesceTick = 50 * time.Millisecond

type obsCoalescer struct {
	mu      sync.Mutex
	pending map[LocoAddr]LocoObservation
	out     chan<- LocoObservation
	tick    time.Duration
	stop    <-chan struct{}
	dropped *atomic.Uint64
}

func newObsCoalescer(out chan<- LocoObservation, tick time.Duration, stop <-chan struct{}, dropped *atomic.Uint64) *obsCoalescer {
	c := &obsCoalescer{
		pending: make(map[LocoAddr]LocoObservation, 32),
		out:     out,
		tick:    tick,
		stop:    stop,
		dropped: dropped,
	}
	go c.loop()
	return c
}

func (c *obsCoalescer) submit(obs LocoObservation) {
	c.mu.Lock()
	if cur, ok := c.pending[obs.Addr]; ok {
		cur.mergeIn(obs)
		c.pending[obs.Addr] = cur
	} else {
		c.pending[obs.Addr] = obs
	}
	c.mu.Unlock()
}

func (c *obsCoalescer) loop() {
	t := time.NewTicker(c.tick)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.flush()
		}
	}
}

func (c *obsCoalescer) flush() {
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return
	}
	batch := c.pending
	c.pending = make(map[LocoAddr]LocoObservation, len(batch))
	c.mu.Unlock()
	for _, obs := range batch {
		select {
		case c.out <- obs:
		default:
			if c.dropped != nil {
				c.dropped.Add(1)
			}
		}
	}
}
