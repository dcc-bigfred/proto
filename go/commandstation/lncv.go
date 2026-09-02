package commandstation

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
)

// LNCV (Uhlenbrock LocoNet Configuration Variable) helpers.
// Message layout follows JMRI LncvMessageContents — see
// java/src/jmri/jmrix/loconet/uhlenbrock/LncvMessageContents.java

const (
	lnOPC_PEER_XFER = 0xE5

	lnLncvLen              = 0x0F
	lnLncvSrcCS            = 0x01
	lnLncvDstModule        = 0x05
	lnLncvCmdWrite         = 0x20
	lnLncvCmdRead          = 0x21
	lnLncvCmdReadReply     = 0x1F
	lnLncvDataProgOn       = 0x80
	lnLncvDataProgOff      = 0x40
	lnLncvAckOpcImmPacket  = 0x6D // LONG_ACK byte 1 for OPC_IMM_PACKET (0xED & 0x7F)
	lnLncvAckWriteOK       = 0x7F
)

type lncvCommand struct {
	opc     byte
	cmd     byte
	cmdData byte
}

var (
	lncvWrite     = lncvCommand{opc: lnOPC_IMM_PACKET, cmd: lnLncvCmdWrite, cmdData: 0x00}
	lncvRead      = lncvCommand{opc: lnOPC_IMM_PACKET, cmd: lnLncvCmdRead, cmdData: 0x00}
	lncvProgStart = lncvCommand{opc: lnOPC_IMM_PACKET, cmd: lnLncvCmdRead, cmdData: lnLncvDataProgOn}
	lncvProgEnd   = lncvCommand{opc: lnOPC_PEER_XFER, cmd: lnLncvCmdRead, cmdData: lnLncvDataProgOff}
)

type lncvFields struct {
	article int
	cv      int
	module  int
}

func lnBuildLncvMessage(c lncvCommand, f lncvFields) []byte {
	pxct1 := byte(0)
	if f.article&0x80 != 0 {
		pxct1 |= 0x01
	}
	if f.article&0x8000 != 0 {
		pxct1 |= 0x02
	}
	if f.cv&0x80 != 0 {
		pxct1 |= 0x04
	}
	if f.cv&0x8000 != 0 {
		pxct1 |= 0x08
	}
	if f.module&0x80 != 0 {
		pxct1 |= 0x10
	}
	if f.module&0x8000 != 0 {
		pxct1 |= 0x20
	}
	if c.cmdData&0x80 != 0 {
		pxct1 |= 0x40
	}

	msg := []byte{
		c.opc,
		lnLncvLen,
		lnLncvSrcCS,
		byte(f.module & 0xFF), // dst_l — always lnLncvDstModule for module ops; overwritten below
		byte((f.module >> 8) & 0xFF),
		c.cmd,
		pxct1,
		byte(f.article & 0x7F),
		byte((f.article >> 8) & 0x7F),
		byte(f.cv & 0x7F),
		byte((f.cv >> 8) & 0x7F),
		byte(f.module & 0x7F),
		byte((f.module >> 8) & 0x7F),
		byte(c.cmdData & 0x7F),
	}
	// Destination for module programming traffic is fixed at 0x0005 (JMRI createLncvMessage source=1 dest=5).
	msg[3] = lnLncvDstModule
	msg[4] = 0x00
	return lnAppendChecksum(msg)
}

func lnBuildLncvModProgStart(article, moduleAddr int) []byte {
	return lnBuildLncvMessage(lncvProgStart, lncvFields{article: article, module: moduleAddr})
}

func lnBuildLncvModProgEnd(article, moduleAddr int) []byte {
	return lnBuildLncvMessage(lncvProgEnd, lncvFields{article: article, module: moduleAddr})
}

func lnBuildLncvCvWrite(article, cvNum, value int) []byte {
	return lnBuildLncvMessage(lncvWrite, lncvFields{article: article, cv: cvNum, module: value})
}

func lnBuildLncvCvRead(article, moduleAddr, cvNum int) []byte {
	return lnBuildLncvMessage(lncvRead, lncvFields{article: article, cv: cvNum, module: moduleAddr})
}

func isLncvMessage(pkt []byte) bool {
	if len(pkt) != 15 {
		return false
	}
	if pkt[1] != lnLncvLen {
		return false
	}
	if pkt[0] != lnOPC_PEER_XFER && pkt[0] != lnOPC_IMM_PACKET {
		return false
	}
	src := pkt[2]
	if src != lnLncvSrcCS && src != 0x05 && src != 0x08 {
		return false
	}
	return isSupportedLncvCmd(pkt[5], pkt[0], int(pkt[13])|lncvCmdDataMSB(pkt[6]))
}

func lncvCmdDataMSB(pxct1 byte) int {
	if pxct1&0x40 != 0 {
		return 0x80
	}
	return 0
}

func isSupportedLncvCmd(cmd byte, opc byte, cmdData int) bool {
	checks := []lncvCommand{lncvWrite, lncvRead, lncvProgStart, lncvProgEnd}
	for _, c := range checks {
		if c.cmd == cmd && c.opc == opc && int(c.cmdData) == cmdData {
			return true
		}
	}
	if cmd == lnLncvCmdReadReply && opc == lnOPC_PEER_XFER && (cmdData == 0 || cmdData == 0x80) {
		return true
	}
	return false
}

type lncvReply struct {
	article int
	cv      int
	value   int
}

func parseLncvReadReply(pkt []byte) (lncvReply, bool) {
	if !lnChecksumOK(pkt) || !isLncvMessage(pkt) {
		return lncvReply{}, false
	}
	cmdData := int(pkt[13]) | lncvCmdDataMSB(pkt[6])
	if pkt[5] != lnLncvCmdReadReply {
		return lncvReply{}, false
	}
	if pkt[0] != lnOPC_PEER_XFER || (cmdData != 0 && cmdData != 0x80) {
		return lncvReply{}, false
	}
	pxct1 := pkt[6]
	art := int(pkt[7]&0x7F) | int(pkt[8]&0x7F)<<8
	if pxct1&0x01 != 0 {
		art |= 0x80
	}
	if pxct1&0x02 != 0 {
		art |= 0x8000
	}
	cv := int(pkt[9]&0x7F) | int(pkt[10]&0x7F)<<8
	if pxct1&0x04 != 0 {
		cv |= 0x80
	}
	if pxct1&0x08 != 0 {
		cv |= 0x8000
	}
	val := int(pkt[11]&0x7F) | int(pkt[12]&0x7F)<<8
	if pxct1&0x10 != 0 {
		val |= 0x80
	}
	if pxct1&0x20 != 0 {
		val |= 0x8000
	}
	return lncvReply{article: art, cv: cv, value: val}, true
}

type lncvWriteAck int

const (
	lncvWriteAckNone lncvWriteAck = iota
	lncvWriteAckOK
	lncvWriteAckRetry // B4 6D 00 — command station busy, resend (JMRI SlotManager)
	lncvWriteAckError
)

func classifyLncvWriteAck(pkt []byte) lncvWriteAck {
	if len(pkt) < 4 || pkt[0] != lnOPC_LONG_ACK {
		return lncvWriteAckNone
	}
	// LOPC is normally 0x6D (0xED & 0x7F); accept full opcode too.
	lopc := pkt[1]
	if lopc != lnLncvAckOpcImmPacket && lopc != lnOPC_IMM_PACKET {
		return lncvWriteAckNone
	}
	switch pkt[2] {
	case lnLncvAckWriteOK:
		return lncvWriteAckOK
	case 0x00:
		return lncvWriteAckRetry
	case 0x01:
		return lncvWriteAckError // CV does not exist
	case 0x02, 0x03:
		return lncvWriteAckError // read-only / out of range
	default:
		return lncvWriteAckError
	}
}

func formatLncvWriteAckError(pkt []byte) error {
	switch pkt[2] {
	case 0x01:
		return errors.New("LNCV write rejected: CV does not exist")
	case 0x02:
		return errors.New("LNCV write rejected: CV is read-only")
	case 0x03:
		return errors.New("LNCV write rejected: value out of range")
	case 0x00:
		return errors.New("LNCV write rejected: command station busy")
	default:
		return fmt.Errorf("LNCV write rejected (LACK ack1=0x%02X)", pkt[2])
	}
}

// NormalizeLncvArticle maps catalogue numbers like 63120 to LNCV article 6312.
func NormalizeLncvArticle(article int) int {
	if article >= 10000 && article%10 == 0 {
		return article / 10
	}
	return article
}

// ErrLncvAppliedNoAck signals that an LNCV write was placed on a confirmed-live
// programming session but the target never acknowledged it. For self-config
// writes to the adapter itself (e.g. Uhlenbrock 63120 CV2 baud, CV4 mode) this
// is expected: the adapter applies the change and reconfigures its own link, so
// the acknowledge is lost. Callers should treat this as "probably applied".
var ErrLncvAppliedNoAck = errors.New("LNCV write sent on a live session but not acknowledged")

// SetLNCV writes one LNCV on a LocoNet module (Uhlenbrock protocol).
// Opens a module programming session, writes the CV, then closes the session.
func (l *LocoNet) SetLNCV(article, moduleAddr, cvNum, value int) error {
	return l.setLNCV(article, moduleAddr, cvNum, value, false)
}

// SetLNCVSelfConfig writes an LNCV on the adapter itself (e.g. the Uhlenbrock
// 63120's own CV2/CV4). Such writes reconfigure the adapter's USB link and are
// not acknowledged, so a missing LACK on a confirmed-live session is reported
// as ErrLncvAppliedNoAck rather than a hard failure, and no read-back is done.
func (l *LocoNet) SetLNCVSelfConfig(article, moduleAddr, cvNum, value int) error {
	return l.setLNCV(article, moduleAddr, cvNum, value, true)
}

func (l *LocoNet) setLNCV(article, moduleAddr, cvNum, value int, selfConfig bool) error {
	article = NormalizeLncvArticle(article)
	if article < 0 || article > 0xFFFF {
		return fmt.Errorf("invalid LNCV article %d", article)
	}
	if moduleAddr < 0 || moduleAddr > 65534 {
		return fmt.Errorf("invalid module address %d", moduleAddr)
	}
	if cvNum < 0 || cvNum > 0xFFFF {
		return fmt.Errorf("invalid LNCV number %d", cvNum)
	}
	if value < 0 || value > 0xFFFF {
		return fmt.Errorf("invalid LNCV value %d", value)
	}

	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	deadline := time.Now().Add(l.timeout)

	// Always close the programming session, even on error: a 63120 left in
	// programming mode stops answering further requests until power-cycled.
	defer func() { _ = l.sendLocked(lnBuildLncvModProgEnd(article, moduleAddr)) }()

	startPkt := lnBuildLncvModProgStart(article, moduleAddr)
	if err := l.sendLocked(startPkt); err != nil {
		return err
	}
	// Handbook §5 / JMRI UhlenbrockPacketizer: the 63120 echoes every frame it
	// puts on the bus. Seeing our own ProgStart echoed confirms the TX path to
	// LocoNet is alive; the optional READ_REPLY carries the module address.
	_, _, sawEcho := l.awaitLncvEchoOrReply(deadline, startPkt, article)

	writePkt := lnBuildLncvCvWrite(article, cvNum, value)
	if err := l.sendLocked(writePkt); err != nil {
		return err
	}
	if selfConfig {
		// A self-config write reconfigures the adapter's own link; do not insist
		// on a LACK or a read-back. If the session was confirmed alive, report
		// "applied, no ack"; otherwise surface the real connectivity problem.
		if err := l.awaitLncvWriteAckBest(deadline); err != nil {
			if sawEcho || l.rxByteCount() > 0 {
				return ErrLncvAppliedNoAck
			}
			return l.annotateLncvTimeout(err, sawEcho)
		}
		return nil
	}
	if err := l.awaitLncvWriteResultLocked(deadline, article, moduleAddr, cvNum, value, writePkt); err != nil {
		return l.annotateLncvTimeout(err, sawEcho)
	}
	return nil
}

// awaitLncvWriteAckBest waits for a single positive/negative write ACK without
// retries or read-back, used by self-config writes.
func (l *LocoNet) awaitLncvWriteAckBest(deadline time.Time) error {
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			break
		}
		switch classifyLncvWriteAck(pkt) {
		case lncvWriteAckOK:
			return nil
		case lncvWriteAckError:
			return formatLncvWriteAckError(pkt)
		case lncvWriteAckRetry:
			// adapter busy; keep waiting within the deadline
		}
	}
	return errors.New("timeout waiting for LNCV write acknowledge")
}

// annotateLncvTimeout enriches a bare timeout with the most likely root cause,
// using whether any bytes were seen on the wire at all.
func (l *LocoNet) annotateLncvTimeout(err error, sawEcho bool) error {
	if err == nil {
		return nil
	}
	if l.rxByteCount() == 0 {
		return fmt.Errorf("%w: no bytes received from the adapter — check that the command "+
			"station is powered, the cable is in the LocoNet/L-NET socket, and the LocoNet "+
			"LED on the adapter blinks", err)
	}
	if !sawEcho {
		return fmt.Errorf("%w: adapter saw bus traffic but did not echo our frames — "+
			"the module/article may not be present or LNCV programming is not supported on this path", err)
	}
	return err
}

// ReadLNCV reads one LNCV from a LocoNet module.
func (l *LocoNet) ReadLNCV(article, moduleAddr, cvNum int) (int, error) {
	article = NormalizeLncvArticle(article)
	l.reqMu.Lock()
	defer l.reqMu.Unlock()
	l.beginSync()
	defer l.endSync()

	deadline := time.Now().Add(l.timeout)

	// Always close the programming session, even on error: a 63120 left in
	// programming mode stops answering further prog-start requests until it is
	// power-cycled.
	defer func() { _ = l.sendLocked(lnBuildLncvModProgEnd(article, moduleAddr)) }()

	startPkt := lnBuildLncvModProgStart(article, moduleAddr)
	if err := l.sendLocked(startPkt); err != nil {
		return 0, err
	}
	startRep, gotStart, sawEcho := l.awaitLncvEchoOrReply(deadline, startPkt, article)

	// A prog-start read replies with CV0 (= module address); use it directly
	// instead of issuing a redundant READ the module will not answer again.
	if cvNum == 0 && gotStart && startRep.cv == 0 {
		return startRep.value, nil
	}

	if err := l.sendLocked(lnBuildLncvCvRead(article, moduleAddr, cvNum)); err != nil {
		return 0, err
	}
	rep, err := l.awaitLncvReadReplyRequired(deadline, article, cvNum)
	if err != nil {
		return 0, l.annotateLncvTimeout(err, sawEcho)
	}
	return rep.value, nil
}

func (l *LocoNet) awaitLncvWriteResultLocked(deadline time.Time, article, moduleAddr, cvNum, value int, writePkt []byte) error {
	retries := 0
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			break
		}
		switch classifyLncvWriteAck(pkt) {
		case lncvWriteAckOK:
			return nil
		case lncvWriteAckRetry:
			if retries >= 3 {
				return formatLncvWriteAckError(pkt)
			}
			retries++
			if err := l.sendLocked(writePkt); err != nil {
				return err
			}
			continue
		case lncvWriteAckError:
			return formatLncvWriteAckError(pkt)
		}
		if rep, ok := parseLncvReadReply(pkt); ok {
			if rep.article == article && rep.cv == cvNum && rep.value == value {
				return nil
			}
		}
		logrus.Debugf("loconet LNCV write wait: ignored % X", pkt)
	}
	return l.verifyLncvWriteLocked(deadline, article, moduleAddr, cvNum, value)
}

func (l *LocoNet) verifyLncvWriteLocked(deadline time.Time, article, moduleAddr, cvNum, value int) error {
	if err := l.sendLocked(lnBuildLncvCvRead(article, moduleAddr, cvNum)); err != nil {
		return errors.New("timeout waiting for LNCV write acknowledge")
	}
	rep, err := l.awaitLncvReadReplyRequired(deadline, article, cvNum)
	if err != nil {
		return errors.New("timeout waiting for LNCV write acknowledge")
	}
	if rep.value != value {
		return fmt.Errorf("LNCV write verify failed: CV %d is %d, expected %d", cvNum, rep.value, value)
	}
	return nil
}

// awaitLncvEchoOrReply waits briefly after a sent frame for either the 63120's
// echo of that frame (handbook §5 flow control) or a READ_REPLY. It reports
// whether the echo was seen (TX path to bus is alive) and returns any READ_REPLY
// captured (for a prog-start this carries CV0 = module address).
func (l *LocoNet) awaitLncvEchoOrReply(deadline time.Time, sentPkt []byte, article int) (rep lncvReply, gotReply, sawEcho bool) {
	sub := time.Now().Add(1500 * time.Millisecond)
	if sub.After(deadline) {
		sub = deadline
	}
	for time.Now().Before(sub) {
		pkt, err := l.readPacketUntil(sub)
		if err != nil {
			break
		}
		if bytes.Equal(pkt, sentPkt) {
			sawEcho = true
			continue
		}
		if r, ok := parseLncvReadReply(pkt); ok && (article == 0 || r.article == article) {
			return r, true, true
		}
	}
	return lncvReply{}, false, sawEcho
}

// awaitLncvReadReplyOptional waits briefly for a prog-start READ_REPLY (CV0).
func (l *LocoNet) awaitLncvReadReplyOptional(deadline time.Time, article int) (lncvReply, error) {
	sub := time.Now().Add(1500 * time.Millisecond)
	if sub.After(deadline) {
		sub = deadline
	}
	for time.Now().Before(sub) {
		pkt, err := l.readPacketUntil(sub)
		if err != nil {
			return lncvReply{}, nil
		}
		if rep, ok := parseLncvReadReply(pkt); ok && (article == 0 || rep.article == article) {
			return rep, nil
		}
	}
	return lncvReply{}, nil
}

func (l *LocoNet) awaitLncvReadReplyRequired(deadline time.Time, article, cvNum int) (lncvReply, error) {
	for time.Now().Before(deadline) {
		pkt, err := l.readPacketUntil(deadline)
		if err != nil {
			return lncvReply{}, errors.New("timeout waiting for LNCV read reply")
		}
		if rep, ok := parseLncvReadReply(pkt); ok {
			if article != 0 && rep.article != article {
				continue
			}
			if rep.cv != cvNum {
				continue
			}
			return rep, nil
		}
		logrus.Debugf("loconet LNCV read wait: ignored % X", pkt)
	}
	return lncvReply{}, errors.New("timeout waiting for LNCV read reply")
}
