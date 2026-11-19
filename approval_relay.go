package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/approvalsock"
)

const (
	approvalDirPattern   = "herrscher-approval-"
	approvalSocketName   = "approval.sock"
	privateDirMode       = 0o700
	approvalBindPoll     = 5 * time.Millisecond
	approvalBindDeadline = 5 * time.Second
)

var errApprovalSocketUnbound = errors.New("approval socket is not listening")

type approvalRelay struct {
	dir    string
	path   string
	cancel context.CancelFunc
	served chan error
	mu     sync.Mutex
	turn   *approvalTurn
}

type approvalTurn struct {
	asker contracts.ApprovalAsker
}

func openApprovalRelay(ctx context.Context) (*approvalRelay, error) {
	dir, err := os.MkdirTemp("", approvalDirPattern)
	if err != nil {
		return nil, fmt.Errorf("approval directory: %w", err)
	}
	if err = requirePrivateDir(dir); err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	serveCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	relay := &approvalRelay{
		dir:    dir,
		path:   filepath.Join(dir, approvalSocketName),
		cancel: cancel,
		served: make(chan error, 1),
	}
	go func() {
		relay.served <- approvalsock.Serve(serveCtx, relay.path, relay.answer, approvalsock.WithErrorHandler(reportApprovalError))
	}()
	if err = relay.awaitBound(); err != nil {
		return nil, errors.Join(err, relay.close())
	}
	return relay, nil
}

func requirePrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("approval directory: %w", err)
	}
	if perm := info.Mode().Perm(); perm != privateDirMode {
		return fmt.Errorf("approval directory mode %04o: %w", uint32(perm), approvalsock.ErrInsecureDirectory)
	}
	return nil
}

func (r *approvalRelay) awaitBound() error {
	deadline := time.Now().Add(approvalBindDeadline)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(r.path); err == nil {
			return nil
		}
		select {
		case err := <-r.served:
			r.served <- err
			return errors.Join(errApprovalSocketUnbound, err)
		case <-time.After(approvalBindPoll):
		}
	}
	return errApprovalSocketUnbound
}

func (r *approvalRelay) beginTurn(ctx context.Context, asker contracts.ApprovalAsker) func() {
	turnCtx, cancel := context.WithCancel(ctx)
	turn := &approvalTurn{asker: boundedAsker(turnCtx, asker)}
	r.mu.Lock()
	r.turn = turn
	r.mu.Unlock()
	return func() {
		r.endTurn(turn)
		cancel()
	}
}

func (r *approvalRelay) endTurn(turn *approvalTurn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turn == turn {
		r.turn = nil
	}
}

func (r *approvalRelay) currentTurn() *approvalTurn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turn
}

func (r *approvalRelay) answer(ctx context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
	turn := r.currentTurn()
	if turn == nil {
		return denial(req.ID, newMessages().english(keyApprovalNoTurn))
	}
	answer := turn.asker(ctx, req)
	if answer.ID != req.ID {
		return denial(req.ID, newMessages().english(keyApprovalMismatch))
	}
	return answer
}

func boundedAsker(turnCtx context.Context, asker contracts.ApprovalAsker) contracts.ApprovalAsker {
	return func(askCtx context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
		ctx, cancel := context.WithCancel(askCtx)
		defer cancel()
		stop := context.AfterFunc(turnCtx, cancel)
		defer stop()
		return asker(ctx, req)
	}
}

func denial(id, reason string) contracts.ApprovalAnswer {
	return contracts.ApprovalAnswer{ID: id, Decision: contracts.DecisionDeny, Reason: reason}
}

func (r *approvalRelay) env(base map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+1)
	maps.Copy(merged, base)
	merged[approvalsock.EnvApprovalSocket] = r.path
	return merged
}

func (r *approvalRelay) close() error {
	r.cancel()
	serveErr := <-r.served
	return errors.Join(serveErr, os.RemoveAll(r.dir))
}

func reportApprovalError(err error) {
	slog.Warn("approval socket", "error", err)
}

func runOneShot(ctx context.Context, cmdStr string, env map[string]string, prompt contracts.Prompt) (string, error) {
	asker, asks := contracts.ApprovalAskerFrom(ctx)
	if !asks {
		return runCmd(ctx, cmdStr, env, prompt)
	}
	relay, err := openApprovalRelay(ctx)
	if err != nil {
		return "", err
	}
	defer closeRelayLogged(relay)
	endTurn := relay.beginTurn(ctx, asker)
	defer endTurn()
	return runCmd(ctx, cmdStr, relay.env(env), prompt)
}

func closeRelayLogged(relay *approvalRelay) {
	if err := relay.close(); err != nil {
		slog.Warn("closing the approval socket", "error", err)
	}
}
