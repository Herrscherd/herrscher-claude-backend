package claude

import (
	"context"
	"errors"
	"log/slog"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

// ErrApprovalUnreachable matches a turn refused because its session runs without an approval socket and has no session id to resume it with, so the tool hook could not reach the daemon.
var ErrApprovalUnreachable = errors.New("claude session was launched without an approval socket and cannot be resumed")

type approvalLaunch uint8

const (
	launchBare approvalLaunch = iota + 1
	launchBound
)

func (r *streamResponder) beginApprovalTurn(ctx context.Context, lang i18n.Language) (func(), error) {
	asker, asks := contracts.ApprovalAskerFrom(ctx)
	if !asks {
		return func() {}, nil
	}
	if err := r.upgradeBareSession(lang); err != nil {
		return nil, err
	}
	relay, err := r.approvalRelayFor(ctx)
	if err != nil {
		return nil, err
	}
	return relay.beginTurn(ctx, asker, lang), nil
}

func (r *streamResponder) upgradeBareSession(lang i18n.Language) error {
	if r.launchState() != launchBare {
		return nil
	}
	live := r.currentSession()
	if live == nil {
		return nil
	}
	r.publishSessionID(live.sessID)
	if r.currentSessionID() == "" {
		return newCatalogError(lang, keyApprovalUnreachable, ErrApprovalUnreachable)
	}
	if err := r.dropSession(live); err != nil {
		slog.Warn("closing the bare claude session before relaunching it with the approval socket", "error", err)
	}
	return nil
}

func (r *streamResponder) approvalRelayFor(ctx context.Context) (*approvalRelay, error) {
	installed, err := r.installedRelay()
	if err != nil || installed != nil {
		return installed, err
	}
	fresh, err := openApprovalRelay(ctx)
	if err != nil {
		return nil, err
	}
	return r.installRelay(fresh)
}

func (r *streamResponder) installedRelay() (*approvalRelay, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return nil, ErrResponderClosed
	}
	return r.approvals, nil
}

func (r *streamResponder) installRelay(fresh *approvalRelay) (*approvalRelay, error) {
	r.stateMu.Lock()
	closed := r.closed
	existing := r.approvals
	if !closed && existing == nil {
		r.approvals = fresh
	}
	r.stateMu.Unlock()
	if closed {
		closeRelayLogged(fresh)
		return nil, ErrResponderClosed
	}
	if existing != nil {
		closeRelayLogged(fresh)
		return existing, nil
	}
	return fresh, nil
}

func (r *streamResponder) spawnSession(ctx context.Context, resumeID string) (*streamSession, error) {
	r.stateMu.Lock()
	relay := r.approvals
	r.stateMu.Unlock()
	env, launch := r.env, launchBare
	if relay != nil {
		env, launch = relay.env(r.env), launchBound
	}
	session, err := startStreamSession(ctx, r.base, r.model, resumeID, r.dir, env)
	if err != nil {
		return nil, err
	}
	r.stateMu.Lock()
	r.launch = launch
	r.stateMu.Unlock()
	return session, nil
}

func (r *streamResponder) launchState() approvalLaunch {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.launch
}
