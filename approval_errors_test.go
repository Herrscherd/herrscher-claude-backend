package claude

import (
	"context"
	"errors"
	"fmt"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

func TestOneShotPathTooLongIsReportedAsPathLimit(t *testing.T) {
	t.Parallel()
	cause := fmt.Errorf("%w: %w", errApprovalOpen, ErrApprovalSocketPathTooLong)
	responder := &oneShotResponder{run: func(context.Context, contracts.Prompt) (string, error) {
		return "", cause
	}}
	for _, lang := range []i18n.Language{i18n.English, i18n.French} {
		_, err := responder.Respond(t.Context(), contracts.Prompt{Content: "run the tool", Language: lang}, nil)
		want := newMessages().localized(lang, keyApprovalPathTooLong)
		if err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v, want the path-limit message %q", lang, err, want)
		}
		if !errors.Is(err, ErrApprovalSocketPathTooLong) {
			t.Fatalf("%s: err = %v, want ErrApprovalSocketPathTooLong kept in the chain", lang, err)
		}
	}
}
