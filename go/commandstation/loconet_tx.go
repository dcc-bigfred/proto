package commandstation

import (
	"errors"
)

type lnTxPriority uint8

const (
	lnTxPriorityNormal lnTxPriority = iota
	lnTxPriorityLow
	lnTxPriorityEstop
)

func (p lnTxPriority) isEstop() bool { return p == lnTxPriorityEstop }

type lnTxJob struct {
	pkt  []byte
	done chan error
}

// txLoop is the sole owner of bus pacing and transport writes. Estop frames
// are always drained before normal or keepalive traffic.
func (l *LocoNet) txLoop() {
	var pendingLow *lnTxJob
	for {
		if pendingLow != nil {
			select {
			case <-l.stop:
				return
			case job := <-l.txEstopCh:
				l.txRun(job)
				continue
			case job := <-l.txCh:
				l.txRun(job)
				continue
			default:
				job := *pendingLow
				pendingLow = nil
				l.txRun(job)
				continue
			}
		}

		select {
		case <-l.stop:
			return
		case job := <-l.txEstopCh:
			l.txRun(job)
		case job := <-l.txCh:
			l.txRun(job)
		case job := <-l.txLowCh:
			select {
			case ej := <-l.txEstopCh:
				deferred := job
				pendingLow = &deferred
				l.txRun(ej)
			case nj := <-l.txCh:
				deferred := job
				pendingLow = &deferred
				l.txRun(nj)
			default:
				l.txRun(job)
			}
		}
	}
}

func (l *LocoNet) txRun(job lnTxJob) {
	err := l.txWriteOne(job.pkt)
	if job.done != nil {
		job.done <- err
	}
}

// txEnqueue submits one frame to the writer goroutine and blocks until it has
// been paced and written (or the driver is stopping).
func (l *LocoNet) txEnqueue(pkt []byte, p lnTxPriority) error {
	done := make(chan error, 1)
	job := lnTxJob{pkt: pkt, done: done}
	dest := l.txCh
	switch p {
	case lnTxPriorityLow:
		dest = l.txLowCh
	case lnTxPriorityEstop:
		dest = l.txEstopCh
	}
	if p.isEstop() {
		select {
		case dest <- job:
		case <-l.stop:
			return errors.New("loconet: stopped")
		default:
			return nil
		}
	} else {
		select {
		case dest <- job:
		case <-l.stop:
			return errors.New("loconet: stopped")
		}
	}
	select {
	case err := <-done:
		return err
	case <-l.stop:
		return errors.New("loconet: stopped")
	}
}

// txWriteOne validates, paces, and writes one frame. Only txLoop calls this.
func (l *LocoNet) txWriteOne(pkt []byte) error {
	l.pace(pkt)
	return l.writeRaw(pkt)
}
