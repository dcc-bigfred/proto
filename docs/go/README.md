# Go library — `commandstation`

The [`github.com/dcc-bigfred/proto/go/commandstation`](../../go/commandstation) package provides a shared interface for driving locomotives over Z21, LocoNet, and WiThrottle. Below are minimal examples for connecting, driving, auxiliary functions, and emergency stop.

## Installation

```bash
go get github.com/dcc-bigfred/proto/go/commandstation
```

Requires Go ≥ 1.25.

## The `Station` interface

Every driver implements the same core operations:

| Method | Description |
|--------|-------------|
| `SetSpeed` | Set speed and direction |
| `GetSpeed` | Read speed and direction |
| `SendFn` | Turn a function on or off (F0, F1, …) |
| `ListFunctions` | List active functions |
| `EmergencyStop` | Per-locomotive emergency stop |
| `ReadCV` / `WriteCV` | Decoder programming (not all protocols) |
| `CleanUp` | Close the connection and release resources |

Optional capabilities (type-assert after connecting):

- `TrackPowerController` — turn track power on/off (`SetTrackPower`)
- `StateObserver` — channel of bus state changes (`ObserveStates`)
- `SlotManager` — LocoNet slot lifecycle (LocoNet only)

## Connecting to a command station

Pick the constructor that matches your hardware. Always close with `CleanUp()` (e.g. via `defer`).

### Z21 (UDP, port 21105)

```go
package main

import (
	"log"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

func main() {
	st, err := commandstation.NewZ21Roco("192.168.0.111", 21105)
	if err != nil {
		log.Fatal(err)
	}
	defer st.CleanUp()

	// … drive locomotives …
}
```

### WiThrottle (TCP, default port 12090)

Works with JMRI, DCC-EX, LNWI, RB1110, and similar servers.

```go
st, err := commandstation.NewWiThrottle("192.168.0.42", 12090)
if err != nil {
	log.Fatal(err)
}
defer st.CleanUp()

// Port 0 uses the default 12090:
// st, err := commandstation.NewWiThrottle("192.168.0.42", 0)
```

You can optionally set the client identity:

```go
import "github.com/dcc-bigfred/proto/go/withrottle"

st, err := commandstation.NewWiThrottle("192.168.0.42", 12090,
	withrottle.WithName("my-app"),
	withrottle.WithDeviceID("app-01"),
)
```

### LocoNet

**Serial port** (e.g. Uhlenbrock 63120, PR3):

```go
st, err := commandstation.NewLocoNetSerial("/dev/ttyUSB0", 57600)
```

**TCP — LbServer (ASCII)**, typical port 5550:

```go
st, err := commandstation.NewLocoNetTCP("192.168.0.10", 5550)
```

**TCP — raw LocoNet frames** (RocRail `lbtcp`), port 1234:

```go
st, err := commandstation.NewLocoNetTCPBinary("192.168.0.10", 1234)
```

### LAN autodetection

Scanners return candidate connections with URIs (`udp://…`, `withrottle://…`, `lbserver://…`, `tcp://…`):

```go
import (
	"context"
	"fmt"
	"time"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

func scan(subnet string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scanner := commandstation.MultiAutodetection{
		commandstation.Z21Autodetection{SubnetPrefix: subnet},
		commandstation.WiThrottleAutodetection{SubnetPrefix: subnet},
		commandstation.LocoNetTCPAutodetection{SubnetPrefix: subnet},
	}

	_ = scanner.Scan(ctx, func(c commandstation.DetectedConnection) error {
		fmt.Println(c.Name, c.URI)
		return nil
	})
}
```

After picking a URI, open the matching constructor manually (e.g. `udp://192.168.0.111:21105` → `NewZ21Roco("192.168.0.111", 21105)`).

## Driving — `SetSpeed` / `GetSpeed`

A locomotive address is a `commandstation.LocoAddr`. Speed is on a 0…N scale where N depends on the decoder's step count.

```go
addr := commandstation.LocoAddr(3)

// Drive forward: speed 50 of 128 steps (typical DCC decoder)
err := st.SetSpeed(addr, 50, true, 128)
if err != nil {
	log.Fatal(err)
}

// Normal stop (not e-stop)
err = st.SetSpeed(addr, 0, true, 128)

// Read state
speed, forward, err := st.GetSpeed(addr)
```

`SetSpeed` parameters:

| Parameter | Meaning |
|-----------|---------|
| `addr` | DCC locomotive address |
| `speed` | `0` = stop; `1` on Z21/WiThrottle is a special code (see EmergencyStop); `2`…`127` = driving |
| `forward` | `true` = forward, `false` = reverse |
| `speedSteps` | `14`, `28`, or `128` — must match the decoder configuration |

## Auxiliary functions — `SendFn` / `ListFunctions`

Function numbers are `0` = headlight (F0), `1` = F1, `2` = F2, and so on.

```go
addr := commandstation.LocoAddr(3)
mode := commandstation.MainTrackMode // main-track / PoM operation

// Z21 / LocoNet: toggle is the desired state (true = on, false = off)
err := st.SendFn(mode, addr, 0, true)  // turn F0 (headlight) on
err = st.SendFn(mode, addr, 1, false)  // turn F1 off

// WiThrottle: toggle=false always turns the function ON;
// toggle=true flips the current state in the client cache
err = st.SendFn(mode, addr, 2, false) // turn F2 on
err = st.SendFn(mode, addr, 2, true)  // toggle F2

// Which functions are active?
fns, err := st.ListFunctions(addr) // e.g. []int{0, 2}
```

Supported ranges:

| Driver | Functions |
|--------|-----------|
| Z21 | F0–F31 |
| LocoNet | F0–F28 |
| WiThrottle | F0–F28 (server-dependent) |

WiThrottle returns `commandstation.ErrUnsupported` for `ReadCV` / `WriteCV`.

## Emergency stop — `EmergencyStop`

`EmergencyStop` stops **one** locomotive using the protocol's emergency command (not a global layout e-stop):

| Driver | On-the-wire behaviour |
|--------|----------------------|
| LocoNet | Slot speed `0x01` (emergency) |
| Z21 | `LAN_X_SET_LOCO_DRIVE` with V=1 |
| WiThrottle | `M0A…X` command |

```go
addr := commandstation.LocoAddr(3)

// forward = direction bit kept on the wire (usually the current driving direction)
err := st.EmergencyStop(addr, true)
if err != nil {
	log.Fatal(err)
}
```

On LocoNet, an e-stop against a free slot may briefly acquire a slot only for the duration of the command. If BigFred already holds the slot, it remains held after the e-stop.

## Track power (optional)

Track power is not part of `Station`. Type-assert `TrackPowerController` — only Z21, LocoNet, and WiThrottle implement it (`StubStation` does not). A missing interface means the driver cannot switch power; `ErrTrackPowerUnsupported` is returned by a connected driver that implements the interface but cannot send the command (for example a nil or disconnected Z21).

```go
tp, ok := st.(commandstation.TrackPowerController)
if !ok {
	log.Fatal("station cannot switch track power")
}
if err := tp.SetTrackPower(true); err != nil {
	log.Fatal(err)
}
_ = tp.SetTrackPower(false)
```

Wire verbs: LocoNet `OPC_GPON` / `OPC_GPOFF`, Z21 `LAN_X_SET_TRACK_POWER_*`, WiThrottle `PPA1` / `PPA0`.

## Complete example

```go
package main

import (
	"log"
	"time"

	"github.com/dcc-bigfred/proto/go/commandstation"
)

func main() {
	st, err := commandstation.NewZ21Roco("192.168.0.111", 21105)
	if err != nil {
		log.Fatal(err)
	}
	defer st.CleanUp()

	addr := commandstation.LocoAddr(42)
	mode := commandstation.MainTrackMode

	if err := st.SetSpeed(addr, 30, true, 128); err != nil {
		log.Fatal(err)
	}

	if err := st.SendFn(mode, addr, 0, true); err != nil { // F0 on
		log.Fatal(err)
	}

	time.Sleep(5 * time.Second)

	if err := st.EmergencyStop(addr, true); err != nil {
		log.Fatal(err)
	}

	if fns, err := st.ListFunctions(addr); err != nil {
		log.Fatal(err)
	} else {
		log.Println("active functions:", fns)
	}
}
```

## Observing external throttle changes (optional)

When the driver implements `StateObserver`, you can listen for changes from physical throttles on the same bus:

```go
if obs, ok := st.(commandstation.StateObserver); ok {
	ch := obs.ObserveStates()
	go func() {
		for o := range ch {
			if o.HasSpeed {
				log.Printf("loco %d: speed %d", o.Addr, o.Speed)
			}
		}
	}()
}
```

On older Z21 firmware, also call `SubscribeLocoInfo(addr)` (`LocoInfoSubscriber`) to receive push updates for selected addresses.

## Tests and further reading

- Integration tests in `go/commandstation/*_test.go` and `go/z21/roundtrip_test.go`
- Protocol specifications: [`docs/z21.md`](../z21.md), [`docs/loconet.md`](../loconet.md), [`docs/withrottle.md`](../withrottle.md)
- Rust protocols (no sockets): [`docs/rust/README.md`](../rust/README.md)
- Run tests: `make -C go test` or `make test-go` from the repository root
