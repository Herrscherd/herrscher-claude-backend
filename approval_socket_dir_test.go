package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalSocketDirAbsolutizesRelativeBase(t *testing.T) {
	t.Parallel()
	got, err := approvalSocketDir("relative-base", "", approvalSocketPathLimit)
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
