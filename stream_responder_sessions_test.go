package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

func TestInstallKillsTheSessionItReplaces(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	replaced := &spyWriteCloser{}
	r.sess = newStreamSession(replaced, strings.NewReader(""))
	if !returnWithin(t, "install", func() bool { return r.install(newStreamSession(&spyWriteCloser{}, strings.NewReader(""))) }) {
		t.Fatal("install refused on an open responder")
	}
	if !replaced.closed {
		t.Fatal("the session that install replaced was not closed")
	}
}

func TestClosedResponderNamesItselfInTheSessionLanguage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lang i18n.Language
		want i18n.Language
	}{
		{"unknown language", i18n.Language(0), i18n.English},
		{"english", i18n.English, i18n.English},
		{"french", i18n.French, i18n.French},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newStreamResponder(context.Background(), nil, "", "", "", nil)
			if err := returnWithin(t, "Close", r.Close); err != nil {
				t.Fatalf("Close: %v", err)
			}
			want := mustRender(t, tc.want, keyTurnResponderClosed)
			assertRespondNamesClosedSession(t, r, tc.lang, want)
			assertCompactNamesClosedSession(t, r, tc.lang, want)
		})
	}
}

func assertRespondNamesClosedSession(t *testing.T, r *streamResponder, lang i18n.Language, want string) {
	t.Helper()
	_, err := respondWithin(context.Background(), t, r, contracts.Prompt{Content: "hi", Language: lang}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Respond: errors.Is(err, ErrResponderClosed) = false for %v", err)
	}
	if err.Error() != want {
		t.Fatalf("Respond error = %q, want the catalog text %q", err.Error(), want)
	}
}

func assertCompactNamesClosedSession(t *testing.T, r *streamResponder, lang i18n.Language, want string) {
	t.Helper()
	_, err := compactWithin(context.Background(), t, r, contracts.CompactRequest{Session: contracts.SessionInfo{Language: lang}}, nil)
	if !errors.Is(err, ErrResponderClosed) {
		t.Fatalf("Compact: errors.Is(err, ErrResponderClosed) = false for %v", err)
	}
	if !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("Compact error = %q, want it to end with the catalog text %q", err.Error(), want)
	}
}

func TestReplacedAndDroppedSessionsKeepThePendingCountBalanced(t *testing.T) {
	t.Parallel()
	r := newStreamResponder(context.Background(), nil, "", "", "", nil)
	first := newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	second := newStreamSession(&spyWriteCloser{}, strings.NewReader(""))
	if !returnWithin(t, "install", func() bool { return r.install(first) && r.install(second) }) {
		t.Fatal("install refused on an open responder")
	}
	if err := returnWithin(t, "dropSession", func() error { return r.dropSession(second) }); err != nil {
		t.Fatalf("dropSession: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2 seconds: pending no longer balances to zero")
	}
}
