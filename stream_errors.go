package claude

import (
	"errors"
	"io"
	"strings"

	"github.com/Herrscherd/herrscher-contracts/i18n"
)

// ErrTurnFailed matches every turn that the claude CLI reported as failed.
var ErrTurnFailed = errors.New("claude turn failed")

// ErrResponderClosed matches a Respond or Compact call made after Close, and a turn that Close interrupted.
var ErrResponderClosed = errors.New("claude responder is closed")

var errTurnReported = newCatalogError(i18n.English, keyTurnFailed, nil)

func localizeTurnEnd(lang i18n.Language, err error) error {
	switch {
	case errors.Is(err, ErrResponderClosed):
		return newCatalogError(lang, keyTurnResponderClosed, err)
	case errors.Is(err, io.EOF):
		return newCatalogError(lang, keyTurnStreamEnded, err)
	}
	return err
}

// ErrStreamDrift matches a stream event whose control fields no longer decode.
// The turn is not retried: resending the prompt would duplicate it.
var ErrStreamDrift = errors.New("claude stream event does not match the protocol")

const maxErrorDetailRunes = 2000

func errorDetail(text string) string {
	return truncateRunes(strings.TrimSpace(text), maxErrorDetailRunes)
}

func errFromTurn(turn turnResult, sessID, reported string) error {
	for _, candidate := range []string{turn.ErrMsg, turn.APIErrMsg} {
		if msg := errorDetail(candidate); msg != "" {
			return &turnError{msg: msg, sessID: sessID}
		}
	}
	return &turnError{msg: reported, sessID: sessID}
}

type turnError struct {
	msg    string
	sessID string
}

func (e *turnError) Error() string { return e.msg }

func (e *turnError) Unwrap() error { return ErrTurnFailed }

func (e *turnError) SessionID() string { return e.sessID }
