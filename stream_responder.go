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

type streamResponder struct {
	spawn    func(resumeID string) (*streamSession, error)
	base     []string
	model    string
	env      map[string]string
	dir      string
	idMu     sync.Mutex
	resumeID string
	turnMu   sync.Mutex
	stateMu  sync.Mutex
	sess     *streamSession
	closed   bool
	pending  int
	stopped  chan struct{}
	stopOnce sync.Once
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
	r.idMu.Lock()
	r.resumeID = sessionID
	r.idMu.Unlock()
}

func (r *streamResponder) forgetLostResume(lostID string) {
	r.idMu.Lock()
	forgotten := r.resumeID == lostID
	if forgotten {
		r.resumeID = ""
	}
	r.idMu.Unlock()
	if forgotten {
		slog.Warn("claude resume lost before any line; starting without --resume", "session", lostID)
	}
}

func (r *streamResponder) currentSessionID() string {
	r.idMu.Lock()
	defer r.idMu.Unlock()
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
	session, err := r.liveSession()
	if err != nil {
		return turnResult{}, err
	}
	turn, err := session.Send(ctx, content, onEvent, r.publishSessionID)
	if err != nil {
		return r.retryTurn(ctx, session, content, onEvent, turn, err)
	}
	r.publishSessionID(turn.SessionID)
	return turn, nil
}

func (r *streamResponder) retryTurn(ctx context.Context, failed *streamSession, content string, onEvent func(contracts.BackendEvent), failure turnResult, cause error) (turnResult, error) {
	if r.isClosed() {
		return turnResult{}, ErrResponderClosed
	}
	if failure.acted || errors.Is(cause, ErrStreamDrift) || errors.Is(cause, ErrStreamLineTooLong) || ctx.Err() != nil {
		return turnResult{}, errors.Join(cause, r.dropSession(failed))
	}
	if onEvent != nil {
		onEvent(contracts.BackendEvent{Kind: "reset"})
	}
	closeErr := r.dropSession(failed)
	if failed.resumedFrom != "" && !failed.accepted {
		r.forgetLostResume(failed.resumedFrom)
	}
	turn, err := r.restartAndSend(ctx, content, onEvent)
	if err != nil {
		return turnResult{}, r.closedOr(errors.Join(err, closeErr))
	}
	if closeErr != nil {
		slog.Warn("closing the dead claude session", "error", closeErr)
	}
	r.publishSessionID(turn.SessionID)
	return turn, nil
}

func (r *streamResponder) restartAndSend(ctx context.Context, content string, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	session, err := r.liveSession()
	if err != nil {
		return turnResult{}, err
	}
	turn, err := session.Send(ctx, content, onEvent, r.publishSessionID)
	if err != nil {
		return turnResult{}, errors.Join(err, r.dropSession(session))
	}
	return turn, nil
}

func (r *streamResponder) ResumeToken() string {
	return r.currentSessionID()
}

func (r *streamResponder) NativeSkills() bool { return true }
