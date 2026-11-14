package claude

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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
