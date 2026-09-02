package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/dcc-bigfred/proto/go/drive"
	"github.com/dcc-bigfred/proto/go/withrottle"
	"github.com/dcc-bigfred/proto/go/z21"
)

func main() {
	expect := flag.Int("expect", 1, "exit after N drive events")
	flag.Parse()

	h := &host{
		expect: *expect,
		done:   make(chan struct{}),
	}
	z, err := z21.Listen("127.0.0.1:0", h)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer z.Close()
	w, err := withrottle.Listen("127.0.0.1:0", h)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer w.Close()
	fmt.Printf("Z21=%s\n", z.Addr().String())
	fmt.Printf("WT=%s\n", w.Addr().String())
	_ = os.Stdout.Sync()
	select {
	case <-h.done:
	case <-time.After(15 * time.Second):
		os.Exit(1)
	}
}

type host struct {
	mu     sync.Mutex
	n      int
	expect int
	done   chan struct{}
	once   sync.Once
}

func (h *host) emit(line string) {
	fmt.Println(line)
	_ = os.Stdout.Sync()
	h.mu.Lock()
	h.n++
	n := h.n
	h.mu.Unlock()
	if n >= h.expect {
		h.once.Do(func() { close(h.done) })
	}
}

func (h *host) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, steps uint8) error {
	h.emit(fmt.Sprintf("SetSpeed %d %d %v %d", addr, speed, forward, steps))
	return nil
}
func (h *host) SetFunction(_ drive.ClientID, addr uint16, fn uint8, on bool) error {
	h.emit(fmt.Sprintf("SetFunction %d %d %v", addr, fn, on))
	return nil
}
func (h *host) LocoState(addr uint16) (drive.LocoState, error) {
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
}
func (h *host) SetTrackPower(drive.ClientID, bool) error { return nil }
func (h *host) Release(drive.ClientID, uint16)           {}
