package claude

import (
	"context"
	"errors"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

// ErrApprovalUnreachable matches a turn refused because its session was launched without an approval socket, so the tool hook could not reach the daemon.
var ErrApprovalUnreachable = errors.New("claude session was launched without an approval socket")

func (r *streamResponder) beginApprovalTurn(ctx context.Context, lang i18n.Language) (func(), error) {
	asker, asks := contracts.ApprovalAskerFrom(ctx)
	if !asks {
		return func() {}, nil
	}
	relay, err := r.approvalRelayFor(ctx, lang)
	if err != nil {
		return nil, err
	}
	return relay.beginTurn(ctx, asker), nil
}

func (r *streamResponder) approvalRelayFor(ctx context.Context, lang i18n.Language) (*approvalRelay, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return nil, ErrResponderClosed
	}
	if r.approvals != nil {
		return r.approvals, nil
	}
	if r.launchedBare {
		return nil, newCatalogError(lang, keyApprovalUnreachable, ErrApprovalUnreachable)
	}
	relay, err := openApprovalRelay(ctx)
	if err != nil {
		return nil, err
	}
	r.approvals = relay
	return relay, nil
}

func (r *streamResponder) launchEnv() map[string]string {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.approvals == nil {
		r.launchedBare = true
		return r.env
	}
	return r.approvals.env(r.env)
}
