package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
)

func TestDirFailedQuotesControlCharacters(t *testing.T) {
	t.Parallel()
	dir := "/tmp/a\nb\x1b[31mc"
	cause := fmt.Errorf("%w: %w", errApprovalOpen, &approvalDirError{dir: dir, err: errors.New("boom")})
	for _, lang := range []i18n.Language{i18n.English, i18n.French} {
		err := localizeTurnEnd(lang, cause)
		want := newMessages().localizedWith(lang, keyApprovalDirFailed, approvalDirParams(dir))
		if err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v, want %q", lang, err, want)
		}
		if strings.ContainsAny(err.Error(), "\n\x1b") {
			t.Fatalf("%s: message %q carries a raw newline or escape character", lang, err.Error())
		}
		if !strings.Contains(err.Error(), `\n`) || !strings.Contains(err.Error(), `\x1b`) {
			t.Fatalf("%s: message %q does not show the directory quoted", lang, err.Error())
		}
	}
}

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
