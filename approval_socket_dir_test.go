package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts/i18n"
)

func TestApprovalSocketDirAbsolutizesRelativeBase(t *testing.T) {
	t.Parallel()
	const roomy = 4096
	got, err := approvalSocketDir("relative-base", "", roomy)
	if err != nil {
		t.Fatalf("approvalSocketDir: %v", err)
	}
	want, err := filepath.Abs("relative-base")
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !filepath.IsAbs(got) {
		t.Fatalf("approvalSocketDir = %q, want the absolute %q", got, want)
	}
}

func TestRelayOpensUnderRelativeTMPDIR(t *testing.T) {
	root := shortTempRoot(t)
	rel := relativeFromWorkDir(t, root)
	t.Setenv("TMPDIR", rel)
	relay, err := openApprovalRelay(context.WithoutCancel(t.Context()))
	if err != nil {
		t.Fatalf("openApprovalRelay under %q: %v", rel, err)
	}
	t.Cleanup(func() {
		if err := relay.close(); err != nil {
			t.Errorf("close relay: %v", err)
		}
	})
	if !filepath.IsAbs(relay.path) || !strings.HasPrefix(relay.path, root+string(filepath.Separator)) {
		t.Fatalf("relay path %q is not absolute under %q, the CLI cannot resolve it from its own directory", relay.path, root)
	}
}

func TestRelativeAbsentTMPDIRReportsAbsoluteDirectory(t *testing.T) {
	absent := absentTempRoot(t)
	t.Setenv("TMPDIR", relativeFromWorkDir(t, absent))
	_, err := openApprovalRelay(context.WithoutCancel(t.Context()))
	dirErr, ok := errors.AsType[*approvalDirError](err)
	if !ok {
		t.Fatalf("err = %v, want an approvalDirError", err)
	}
	if dirErr.dir != absent {
		t.Fatalf("reported directory = %q, want the absolute %q", dirErr.dir, absent)
	}
	if _, statErr := os.Stat(absent); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("absent root no longer absent after the conversion: %v", statErr)
	}
}

func TestDeletedWorkDirIsReportedAsDirFailed(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", "relative-base")
	_, err := openApprovalRelay(context.WithoutCancel(t.Context()))
	if _, ok := errors.AsType[*approvalDirError](err); !ok {
		t.Fatalf("err = %v, want an approvalDirError", err)
	}
	cause := fmt.Errorf("%w: %w", errApprovalOpen, err)
	for _, lang := range []i18n.Language{i18n.English, i18n.French} {
		got := localizeTurnEnd(lang, cause)
		want := dirFailedMessage(lang, "relative-base")
		if got == nil || got.Error() != want {
			t.Fatalf("%s: message = %v, want %q", lang, got, want)
		}
	}
}

func relativeFromWorkDir(tb testing.TB, target string) string {
	tb.Helper()
	wd, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	rel, err := filepath.Rel(wd, target)
	if err != nil {
		tb.Fatal(err)
	}
	return rel
}
