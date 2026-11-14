package claude

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

func TestRespondNamesAStreamThatEndsMidTurn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lang i18n.Language
	}{
		{"english", i18n.English},
		{"french", i18n.French},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newStreamResponder(context.Background(), nil, "", "", "", nil)
			r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine))
			r.spawn = func(string) (*streamSession, error) {
				return newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine)), nil
			}

			_, err := r.Respond(context.Background(), contracts.Prompt{Content: "hi", Language: tc.lang}, nil)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("errors.Is(err, io.EOF) = false for %v", err)
			}
			if want := mustRender(t, tc.lang, keyTurnStreamEnded); err.Error() != want {
				t.Fatalf("error = %q, want the catalog text %q", err.Error(), want)
			}
		})
	}
}

func TestReadTurnFailsWhenTheLineChannelCloses(t *testing.T) {
	t.Parallel()
	lines := make(chan readLine)
	close(lines)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := readTurn(ctx, lines, nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readTurn on a closed line channel: err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestCompactNamesAStreamThatEndsMidTurn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		session i18n.Language
		want    i18n.Language
	}{
		{"unknown session language", i18n.Language(0), i18n.English},
		{"english", i18n.English, i18n.English},
		{"french", i18n.French, i18n.French},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newStreamResponder(context.Background(), nil, "", "", "", nil)
			r.sess = newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine))
			r.spawn = func(string) (*streamSession, error) {
				return newStreamSession(&spyWriteCloser{}, strings.NewReader(partialAssistantLine)), nil
			}

			req := contracts.CompactRequest{Session: contracts.SessionInfo{Language: tc.session}}
			_, err := r.Compact(context.Background(), req, nil)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("errors.Is(err, io.EOF) = false for %v", err)
			}
			assertUnavailableReason(t, err, mustRender(t, tc.want, keyTurnStreamEnded))
		})
	}
}
