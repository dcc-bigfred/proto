package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/dcc-bigfred/proto/go/drive"
	"github.com/dcc-bigfred/proto/go/withrottle"
	"github.com/dcc-bigfred/proto/go/z21"
)

// loopback-host listens on Z21 UDP and WiThrottle TCP, prints bind
// addresses, and exits 0 after SetSpeed(addr=3, speed=50).
func main() {
	h := &host{done: make(chan struct{})}
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
	once sync.Once
	done chan struct{}
}

func (h *host) SetSpeed(_ drive.ClientID, addr uint16, speed uint8, forward bool, _ uint8) error {
	if addr == 3 && speed == 50 && forward {
		h.once.Do(func() { close(h.done) })
	}
	return nil
}
func (h *host) SetFunction(drive.ClientID, uint16, uint8, bool) error { return nil }
func (h *host) LocoState(addr uint16) (drive.LocoState, error) {
	return drive.LocoState{Addr: addr, Steps: 128, Forward: true}, nil
}
func (h *host) SetTrackPower(drive.ClientID, bool) error { return nil }
func (h *host) Release(drive.ClientID, uint16)           {}
