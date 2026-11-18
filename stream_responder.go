package claude

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type oneShotResponder struct {
	run func(ctx context.Context, p contracts.Prompt) (string, error)
}

func (o *oneShotResponder) Respond(ctx context.Context, p contracts.Prompt, _ func(contracts.BackendEvent)) (string, error) {
	return o.run(ctx, p)
}

func (o *oneShotResponder) Close() error { return nil }

func (o *oneShotResponder) NativeSkills() bool { return true }

const (
	consecutiveSilentDeathsBeforeFallback = 2
	maxAttemptsPerTurn                    = 3
)

type streamResponder struct {
	spawn        func(resumeID string) (*streamSession, error)
	base         []string
	model        string
	env          map[string]string
	dir          string
	resumeMu     sync.Mutex
	resumeID     string
	acceptedMu   sync.Mutex
	accepted     bool
	silentDeaths int
	turnMu       sync.Mutex
	stateMu      sync.Mutex
	sess         *streamSession
	closed       bool
	pending      int
	stopped      chan struct{}
	stopOnce     sync.Once
}

func newStreamResponder(ctx context.Context, base []string, model, resumeID, dir string, env map[string]string) *streamResponder {
	responder := &streamResponder{base: base, model: model, resumeID: resumeID, dir: dir, env: env, stopped: make(chan struct{})}
	responder.spawn = func(id string) (*streamSession, error) {
		return startStreamSession(ctx, responder.base, responder.model, id, responder.dir, responder.env)
	}
	return responder
}

func (r *streamResponder) publishSessionID(sessionID string) {
	if sessionID == "" {
		return
	}
	if r.setResumeID(sessionID) {
		r.silentDeaths = 0
	}
}

func (r *streamResponder) setResumeID(sessionID string) bool {
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()
	changed := r.resumeID != sessionID
	r.resumeID = sessionID
	return changed
}

func (r *streamResponder) forgetResume() {
	r.resumeMu.Lock()
	lostID := r.resumeID
	r.resumeID = ""
	r.resumeMu.Unlock()
	slog.Warn("claude resume lost before any line; starting without --resume", "session", lostID)
}

func (r *streamResponder) markAccepted() {
	r.acceptedMu.Lock()
	r.accepted = true
	r.acceptedMu.Unlock()
}

func (r *streamResponder) hasAccepted() bool {
	r.acceptedMu.Lock()
	defer r.acceptedMu.Unlock()
	return r.accepted
}

func refusedBeforeAnyLine(session *streamSession, turn turnResult) bool {
	return turn.IsError && !turn.acted && turn.opening && session.resumedFrom != ""
}

func (r *streamResponder) send(ctx context.Context, session *streamSession, content string, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	turn, err := session.Send(ctx, content, onEvent, r.publishSessionID)
	if turn.acted {
		r.markAccepted()
		r.silentDeaths = 0
	}
	return turn, err
}

func (r *streamResponder) currentSessionID() string {
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()
	return r.resumeID
}

func (r *streamResponder) Respond(ctx context.Context, prompt contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	r.turnMu.Lock()
	defer r.turnMu.Unlock()
	turn, err := r.runTurn(ctx, turnContent(prompt), onEvent)
	if err != nil {
		return "", localizeTurnEnd(prompt.Language, err)
	}
	if turn.IsError {
		return turn.Text, errFromTurn(turn, turn.SessionID, localizedTurnFailure(prompt.Language))
	}
	return turn.Text, nil
}

func (r *streamResponder) runTurn(ctx context.Context, content string, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	if r.isClosed() {
		return turnResult{}, ErrResponderClosed
	}
	if err := ctx.Err(); err != nil {
		return turnResult{}, err
	}
	var deadErr error
	for attempt := 1; ; attempt++ {
		session, err := r.liveSession()
		if err != nil {
			return turnResult{}, joinDead(err, deadErr)
		}
		turn, err := r.send(ctx, session, content, onEvent)
		if succeeded(session, turn, err) {
			return r.finishTurn(turn, deadErr), nil
		}
		if r.isClosed() {
			return turnResult{}, ErrResponderClosed
		}
		if endsTurn(ctx, turn, err) || !mayRelaunch(session, attempt) {
			return r.giveUp(session, turn, err, deadErr)
		}
		deadErr = errors.Join(deadErr, r.recoverFrom(session, turn, onEvent))
	}
}

func succeeded(session *streamSession, turn turnResult, err error) bool {
	return err == nil && !refusedBeforeAnyLine(session, turn)
}

func (r *streamResponder) finishTurn(turn turnResult, deadErr error) turnResult {
	r.publishSessionID(turn.SessionID)
	if deadErr != nil {
		slog.Warn("closing the dead claude session", "error", deadErr)
	}
	return turn
}

func endsTurn(ctx context.Context, failure turnResult, cause error) bool {
	return failure.acted || errors.Is(cause, ErrStreamDrift) || errors.Is(cause, ErrStreamLineTooLong) || ctx.Err() != nil
}

func mayRelaunch(failed *streamSession, attempt int) bool {
	return attempt < maxAttemptsPerTurn && (attempt == 1 || failed.resumedFrom != "")
}

func (r *streamResponder) giveUp(session *streamSession, turn turnResult, cause, deadErr error) (turnResult, error) {
	dropErr := r.dropSession(session)
	if cause == nil {
		return turn, joinDead(dropErr, deadErr)
	}
	return turnResult{}, r.closedOr(joinDead(cause, errors.Join(deadErr, dropErr)))
}

func joinDead(err, deadErr error) error {
	if deadErr == nil {
		return err
	}
	return errors.Join(err, deadErr)
}

func (r *streamResponder) recoverFrom(failed *streamSession, failure turnResult, onEvent func(contracts.BackendEvent)) error {
	refused := refusedBeforeAnyLine(failed, failure)
	if failed.resumedFrom != "" && !refused {
		r.silentDeaths++
	}
	lost := failed.resumedFrom != "" && (refused || !r.hasAccepted() || r.silentDeaths >= consecutiveSilentDeathsBeforeFallback)
	var closeErr error
	if lost {
		r.silentDeaths = 0
		r.forgetResume()
		closeErr = r.discardSession(failed)
	} else {
		closeErr = r.dropSession(failed)
	}
	if onEvent != nil {
		if lost {
			onEvent(resumeLostNotice())
		}
		onEvent(contracts.BackendEvent{Kind: "reset"})
	}
	return closeErr
}

func resumeLostNotice() contracts.BackendEvent {
	return contracts.BackendEvent{
		Kind:   contracts.BackendKindNotice,
		Notice: &contracts.Notice{Code: contracts.CodeResumeLost},
	}
}

func (r *streamResponder) ResumeToken() string {
	return r.currentSessionID()
}

func (r *streamResponder) NativeSkills() bool { return true }
