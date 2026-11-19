package claude

import (
	"errors"
	"log/slog"
	"time"
)

func (r *streamResponder) liveSession() (*streamSession, error) {
	r.stateMu.Lock()
	if r.closed {
		r.stateMu.Unlock()
		return nil, ErrResponderClosed
	}
	if r.sess != nil {
		session := r.sess
		r.stateMu.Unlock()
		return session, nil
	}
	r.pending++
	r.stateMu.Unlock()
	spawned, err := r.spawn(r.currentSessionID())
	if err != nil {
		r.endPending()
		return nil, err
	}
	if !r.install(spawned) {
		return nil, errors.Join(ErrResponderClosed, r.reap(spawned))
	}
	r.endPending()
	return spawned, nil
}

func (r *streamResponder) install(session *streamSession) bool {
	r.stateMu.Lock()
	if r.closed {
		r.stateMu.Unlock()
		return false
	}
	replaced := r.sess
	r.sess = session
	if replaced != nil {
		r.pending++
	}
	r.stateMu.Unlock()
	if replaced != nil {
		if err := r.reap(replaced); err != nil {
			slog.Warn("closing the claude session that install replaced", "error", err)
		}
	}
	return true
}

func (r *streamResponder) currentSession() *streamSession {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.sess
}

func (r *streamResponder) isClosed() bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.closed
}

func (r *streamResponder) closedOr(err error) error {
	if r.isClosed() {
		return ErrResponderClosed
	}
	return err
}

func (r *streamResponder) dropSession(session *streamSession) error {
	r.publishSessionID(session.sessID)
	return r.discardSession(session)
}

func (r *streamResponder) discardSession(session *streamSession) error {
	r.stateMu.Lock()
	if r.sess == session {
		r.sess = nil
	}
	r.pending++
	r.stateMu.Unlock()
	return r.reap(session)
}

func (r *streamResponder) reap(session *streamSession) error {
	err := session.Close()
	r.endPending()
	return err
}

func (r *streamResponder) endPending() {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.pending--
	r.settleLocked()
}

func (r *streamResponder) settleLocked() {
	if r.closed && r.pending == 0 {
		r.stopOnce.Do(func() { close(r.stopped) })
	}
}

func (r *streamResponder) Close() error {
	r.stateMu.Lock()
	r.closed = true
	session := r.sess
	r.sess = nil
	if session != nil {
		r.pending++
	}
	relay := r.approvals
	r.approvals = nil
	r.settleLocked()
	r.stateMu.Unlock()
	var closeErr error
	if session != nil {
		closeErr = r.reap(session)
	}
	var relayErr error
	if relay != nil {
		relayErr = relay.close()
	}
	return errors.Join(closeErr, relayErr, r.awaitStopped())
}

func (r *streamResponder) awaitStopped() error {
	timer := time.NewTimer(teardownBound)
	defer timer.Stop()
	select {
	case <-r.stopped:
		return nil
	case <-timer.C:
		return errTeardownTimedOut
	}
}

const teardownBound = 5 * time.Second

var errTeardownTimedOut = errors.New("claude responder gave up waiting for a spawn in flight to be reaped")
