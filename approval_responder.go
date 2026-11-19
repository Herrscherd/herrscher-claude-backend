package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

// ErrApprovalUnreachable matches a turn refused because its session runs without an approval socket and has no usable session id to resume it with, so the tool hook could not reach the daemon.
var ErrApprovalUnreachable = errors.New("claude session was launched without an approval socket and cannot be resumed")

func (r *streamResponder) beginApprovalTurn(ctx context.Context, lang i18n.Language) (func(), error) {
	asker, asks := contracts.ApprovalAskerFrom(ctx)
	if !asks {
		return func() {}, nil
	}
	relay, err := r.approvalRelayFor(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.upgradeBareSession(relay, lang); err != nil {
		return nil, err
	}
	return relay.beginTurn(ctx, asker, lang), nil
}

func (r *streamResponder) upgradeBareSession(relay *approvalRelay, lang i18n.Language) error {
	live := r.currentSession()
	if live == nil || live.relay == relay {
		return nil
	}
	r.publishSessionID(live.sessID)
	if !validResumeID(r.currentSessionID()) {
		return newCatalogError(lang, keyApprovalUnreachable, ErrApprovalUnreachable)
	}
	if err := r.dropSession(live); err != nil {
		slog.Warn("closing the claude session before relaunching it with the approval socket", "error", err)
	}
	return nil
}

func (r *streamResponder) approvalRelayFor(ctx context.Context) (*approvalRelay, error) {
	installed, err := r.installedRelay()
	if err != nil {
		return nil, err
	}
	if installed != nil && installed.serving() {
		return installed, nil
	}
	fresh, err := openApprovalRelay(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errApprovalOpen, err)
	}
	return r.installRelay(fresh, installed)
}

func (r *streamResponder) launchRelay(ctx context.Context) (*approvalRelay, error) {
	r.stateMu.Lock()
	relay := r.approvals
	r.stateMu.Unlock()
	if relay == nil || relay.serving() {
		return relay, nil
	}
	return r.approvalRelayFor(ctx)
}

func (r *streamResponder) installedRelay() (*approvalRelay, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return nil, ErrResponderClosed
	}
	return r.approvals, nil
}

func (r *streamResponder) installRelay(fresh, stale *approvalRelay) (*approvalRelay, error) {
	r.stateMu.Lock()
	closed := r.closed
	swapped := !closed && r.approvals == stale
	if swapped {
		r.approvals = fresh
	}
	installed := r.approvals
	r.stateMu.Unlock()
	if closed {
		closeRelayLogged(fresh)
		return nil, ErrResponderClosed
	}
	if !swapped {
		closeRelayLogged(fresh)
		return installed, nil
	}
	if stale != nil {
		closeRelayLogged(stale)
	}
	return fresh, nil
}

func (r *streamResponder) spawnSession(ctx context.Context, resumeID string) (*streamSession, error) {
	relay, err := r.launchRelay(ctx)
	if err != nil {
		return nil, err
	}
	env := r.env
	if relay != nil {
		env = relay.env(r.env)
	}
	usable := usableResumeID(resumeID)
	session, err := startStreamSession(ctx, r.base, r.model, usable, r.dir, env)
	if err != nil {
		return nil, err
	}
	session.relay = relay
	if usable != resumeID {
		r.discardResumeID(resumeID)
		session.discardedResume = resumeID
	}
	return session, nil
}
