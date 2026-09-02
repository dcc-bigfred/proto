package commandstation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// DetectedConnection is one discovered command-station connection option
// suitable for BigFred's catalogue (display name + connection URI).
type DetectedConnection struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// EmitFunc is called for each discovered connection as soon as it is found.
// Returning a non-nil error aborts the scanner that invoked it.
type EmitFunc func(DetectedConnection) error

// Autodetection discovers available command-station connections for a
// single transport family (serial, LocoNet TCP, Z21, …). Callers compose
// multiple implementations when scanning everything.
type Autodetection interface {
	Scan(ctx context.Context, emit EmitFunc) error
}

// MultiAutodetection runs several Autodetection implementations in parallel
// and forwards each hit through emit. Individual scanner errors do not
// cancel siblings; all errors are joined and returned after every scanner
// finishes. Partial results already emitted are kept.
type MultiAutodetection []Autodetection

func (m MultiAutodetection) Scan(ctx context.Context, emit EmitFunc) error {
	if emit == nil {
		emit = func(DetectedConnection) error { return nil }
	}

	var (
		emitMu sync.Mutex
		errMu  sync.Mutex
		errs   []error
		wg     sync.WaitGroup
	)
	safeEmit := func(c DetectedConnection) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(c)
	}

	for _, a := range m {
		if a == nil {
			continue
		}
		wg.Add(1)
		go func(a Autodetection) {
			defer wg.Done()
			if err := a.Scan(ctx, safeEmit); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// TCPDialer opens a TCP connection to address (host:port). Overridable in tests.
type TCPDialer func(ctx context.Context, address string) (net.Conn, error)

func defaultTCPDialer(timeout time.Duration) TCPDialer {
	return func(ctx context.Context, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "tcp", address)
	}
}

// UDPProber sends a UDP datagram to address and waits for any reply.
// Overridable in tests.
type UDPProber func(ctx context.Context, address string, payload []byte) error

func defaultUDPProber(timeout time.Duration) UDPProber {
	return func(ctx context.Context, address string, payload []byte) error {
		d := net.Dialer{Timeout: timeout}
		conn, err := d.DialContext(ctx, "udp", address)
		if err != nil {
			return err
		}
		defer conn.Close()
		deadline := time.Now().Add(timeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)
		if _, err := conn.Write(payload); err != nil {
			return err
		}
		buf := make([]byte, 64)
		_, err = conn.Read(buf)
		return err
	}
}

const (
	defaultLANDialTimeout = 1 * time.Second
	defaultLANWorkers     = 20
)

// scanTCPHosts probes hostPrefix.1–255 for openTCP ports in parallel.
// onOpen is called for each successful dial (hostIP, port).
// context.DeadlineExceeded is treated as a normal end of the scan window
// (returns nil), not as a scanner failure.
func scanTCPHosts(
	ctx context.Context,
	hostPrefix string,
	ports []int,
	dial TCPDialer,
	onOpen func(host string, port int),
) error {
	if hostPrefix == "" {
		return fmt.Errorf("commandstation: empty host prefix")
	}
	if dial == nil {
		dial = defaultTCPDialer(defaultLANDialTimeout)
	}

	type job struct {
		host string
		port int
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	workers := defaultLANWorkers
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					continue
				}
				addr := fmt.Sprintf("%s:%d", j.host, j.port)
				conn, err := dial(ctx, addr)
				if err != nil {
					continue
				}
				_ = conn.Close()
				onOpen(j.host, j.port)
			}
		}()
	}

loop:
	for i := 1; i <= 255; i++ {
		host := fmt.Sprintf("%s.%d", hostPrefix, i)
		for _, port := range ports {
			select {
			case <-ctx.Done():
				break loop
			case jobs <- job{host: host, port: port}:
			}
		}
	}
	close(jobs)
	wg.Wait()
	return scanContextError(ctx)
}

func scanContextError(ctx context.Context) error {
	err := ctx.Err()
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}
