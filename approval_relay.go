package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/approvalsock"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const (
	approvalDirPattern      = "herrscher-approval-"
	approvalSocketName      = "approval.sock"
	maxApprovalDirSuffixLen = 10
	privateDirMode          = 0o700
	approvalBindPoll        = 5 * time.Millisecond
	approvalBindDeadline    = 5 * time.Second
)

var errApprovalSocketUnbound = errors.New("approval socket is not listening")

var errApprovalOpen = errors.New("approval socket could not be opened")

type approvalDirError struct {
	dir string
	err error
}

func (e *approvalDirError) Error() string {
	return fmt.Sprintf("approval directory under %s: %v", e.dir, e.err)
}

func (e *approvalDirError) Unwrap() error {
	return e.err
}

func approvalDirParams(dir string) map[string]any {
	return map[string]any{"Dir": strconv.Quote(dir)}
}

// ErrApprovalSocketPathTooLong matches a turn refused because the approval socket path exceeds the socket limit of the platform, even under the fallback directory.
var ErrApprovalSocketPathTooLong = errors.New("approval socket path is longer than the socket limit")

type approvalRelay struct {
	dir      string
	path     string
	cancel   context.CancelFunc
	done     chan struct{}
	serveErr error
	mu       sync.Mutex
	turn     *approvalTurn
	lang     i18n.Language
}

type approvalTurn struct {
	asker contracts.ApprovalAsker
	lang  i18n.Language
}

func openApprovalRelay(ctx context.Context) (*approvalRelay, error) {
	base, err := approvalSocketDir(os.TempDir(), approvalFallbackDir, approvalSocketPathLimit)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, approvalDirPattern)
	if err != nil {
		return nil, &approvalDirError{dir: base, err: err}
	}
	if err = requirePrivateDir(dir); err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	serveCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	relay := &approvalRelay{
		dir:    dir,
		path:   filepath.Join(dir, approvalSocketName),
		cancel: cancel,
		done:   make(chan struct{}),
		lang:   i18n.English,
	}
	go func() {
		relay.serveErr = approvalsock.Serve(serveCtx, relay.path, relay.answer, approvalsock.WithErrorHandler(reportApprovalError))
		close(relay.done)
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

func approvalSocketDir(base, fallback string, limit int) (string, error) {
	absolute, err := filepath.Abs(base)
	if err != nil {
		return "", &approvalDirError{dir: base, err: err}
	}
	switch {
	case fitsApprovalSocket(absolute, limit):
		return absolute, nil
	case fallback != "" && fitsApprovalSocket(fallback, limit):
		return fallback, nil
	}
	return "", fmt.Errorf("approval socket under %s: %w", absolute, ErrApprovalSocketPathTooLong)
}

func fitsApprovalSocket(dir string, limit int) bool {
	longestPath := filepath.Join(dir, approvalDirPattern+strings.Repeat("0", maxApprovalDirSuffixLen), approvalSocketName)
	return len(longestPath) < limit
}

func (r *approvalRelay) awaitBound() error {
	deadline := time.Now().Add(approvalBindDeadline)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(r.path); err == nil {
			return nil
		}
		select {
		case <-r.done:
			return errors.Join(errApprovalSocketUnbound, r.serveErr)
		case <-time.After(approvalBindPoll):
		}
	}
	return errApprovalSocketUnbound
}

func (r *approvalRelay) serving() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func (r *approvalRelay) beginTurn(ctx context.Context, asker contracts.ApprovalAsker, lang i18n.Language) func() {
	turnCtx, cancel := context.WithCancel(ctx)
	turn := &approvalTurn{asker: boundedAsker(turnCtx, asker), lang: lang}
	r.mu.Lock()
	r.turn = turn
	r.lang = lang
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

func (r *approvalRelay) current() (*approvalTurn, i18n.Language) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turn, r.lang
}

func (r *approvalRelay) answer(ctx context.Context, req contracts.ApprovalRequest) contracts.ApprovalAnswer {
	turn, lang := r.current()
	if turn == nil {
		return denial(req.ID, newMessages().localized(lang, keyApprovalNoTurn))
	}
	answer := turn.asker(ctx, req)
	if answer.ID != req.ID {
		return denial(req.ID, newMessages().localized(turn.lang, keyApprovalMismatch))
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
	<-r.done
	return errors.Join(withoutOwnStop(r.serveErr), os.RemoveAll(r.dir))
}

type multiUnwrapper interface {
	error
	Unwrap() []error
}

func withoutOwnStop(err error) error {
	if err != nil && onlyOwnStop(err) {
		return nil
	}
	return err
}

func onlyOwnStop(err error) bool {
	if joined, ok := errors.AsType[multiUnwrapper](err); ok {
		for _, part := range joined.Unwrap() {
			if !onlyOwnStop(part) {
				return false
			}
		}
		return true
	}
	if inner := errors.Unwrap(err); inner != nil {
		return onlyOwnStop(inner)
	}
	return errors.Is(err, context.Canceled)
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
		return "", fmt.Errorf("%w: %w", errApprovalOpen, err)
	}
	defer closeRelayLogged(relay)
	endTurn := relay.beginTurn(ctx, asker, prompt.Language)
	defer endTurn()
	return runCmd(ctx, cmdStr, relay.env(env), prompt)
}

func closeRelayLogged(relay *approvalRelay) {
	if err := relay.close(); err != nil {
		slog.Warn("closing the approval socket", "error", err)
	}
}
