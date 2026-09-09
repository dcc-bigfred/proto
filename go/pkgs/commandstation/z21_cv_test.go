package commandstation

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcc-bigfred/proto/go/pkgs/z21"
)

func TestWriteCV_awaitsResultAndVerifiesWithoutSecondRead(t *testing.T) {
	z, peer := startCVPeer(t, func(req []byte) []byte {
		cv, value, ok := z21.ParseProgWrite(req)
		if !ok {
			t.Errorf("not a prog write: % X", req)
			return nil
		}
		return z21.BuildCvResult(cv+1, value)
	})
	defer z.CleanUp()
	defer peer.Close()

	err := z.WriteCV(ProgrammingTrackMode, LocoCV{
		Cv: CV{Num: 1, Value: 7},
	}, Verify(true), Timeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteCV_nackIsError(t *testing.T) {
	z, peer := startCVPeer(t, func(req []byte) []byte {
		return z21.BuildCvNack()
	})
	defer z.CleanUp()
	defer peer.Close()

	err := z.WriteCV(ProgrammingTrackMode, LocoCV{
		Cv: CV{Num: 17, Value: 0},
	}, Timeout(300*time.Millisecond))
	if err == nil {
		t.Fatal("expected NACK error")
	}
}

func TestWriteCV_pomDoesNotWait(t *testing.T) {
	z, peer := startCVPeer(t, func(req []byte) []byte {
		return nil
	})
	defer z.CleanUp()
	defer peer.Close()

	err := z.WriteCV(MainTrackMode, LocoCV{
		LocoId: 3,
		Cv:     CV{Num: 29, Value: 6},
	}, Verify(true), Timeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteCV_unrecognizedMode(t *testing.T) {
	z, peer := startCVPeer(t, func(req []byte) []byte {
		t.Errorf("unexpected request: % X", req)
		return nil
	})
	defer z.CleanUp()
	defer peer.Close()

	err := z.WriteCV(Mode("other"), LocoCV{Cv: CV{Num: 1, Value: 1}})
	if err == nil || err.Error() != "unrecognized mode" {
		t.Fatalf("got %v", err)
	}
}

func TestWriteCV_retriesTimeoutOnly(t *testing.T) {
	var writes atomic.Int32
	z, peer := startCVPeer(t, func(req []byte) []byte {
		n := writes.Add(1)
		if n < 2 {
			return nil
		}
		cv, value, ok := z21.ParseProgWrite(req)
		if !ok {
			t.Errorf("not a prog write: % X", req)
			return nil
		}
		return z21.BuildCvResult(cv+1, value)
	})
	defer z.CleanUp()
	defer peer.Close()

	err := z.WriteCV(ProgrammingTrackMode, LocoCV{
		Cv: CV{Num: 1, Value: 7},
	}, Timeout(80*time.Millisecond), Retries(1))
	if err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 2 {
		t.Fatalf("writes=%d, want 2", writes.Load())
	}

	writes.Store(0)
	err = z.WriteCV(ProgrammingTrackMode, LocoCV{
		Cv: CV{Num: 1, Value: 7},
	}, Timeout(80*time.Millisecond), Retries(0))
	if err == nil || !errors.Is(err, ErrResponseTimeout) {
		t.Fatalf("retries=0: %v", err)
	}
	if writes.Load() != 1 {
		t.Fatalf("retries=0 writes=%d, want 1", writes.Load())
	}
}

func startCVPeer(t *testing.T, reply func(req []byte) []byte) (*Z21Roco, *net.UDPConn) {
	t.Helper()
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := net.DialUDP("udp", nil, peer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	z := &Z21Roco{
		conn:    cli,
		Timeout: time.Second,
		syncCh:  make(chan []byte, 64),
		obsCh:   make(chan LocoObservation, 8),
		stop:    make(chan struct{}),
		metrics: newZ21Metrics(),
	}
	go z.readLoop()
	go func() {
		buf := make([]byte, 1500)
		for {
			_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, addr, err := peer.ReadFromUDP(buf)
			if err != nil {
				return
			}
			resp := reply(append([]byte(nil), buf[:n]...))
			if resp == nil {
				continue
			}
			_, _ = peer.WriteToUDP(resp, addr)
		}
	}()
	return z, peer
}
