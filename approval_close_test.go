package claude

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertCloseResult(t *testing.T, serveErr, want error, texts []string, err error) {
	t.Helper()
	if want == nil && err != nil {
		t.Fatalf("close = %v, want nil", err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("close = %v, want %v", err, want)
	}
	for _, text := range texts {
		if err == nil || !strings.Contains(err.Error(), text) {
			t.Fatalf("close = %v, want a message containing %q", err, text)
		}
	}
	if texts != nil && !errors.Is(err, serveErr) {
		t.Fatalf("close = %v, want the original error %v", err, serveErr)
	}
}

func TestRelayCloseDropsOnlyItsOwnStop(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	cases := []struct {
		name     string
		serveErr error
		want     error
		texts    []string
	}{
		{name: "own stop", serveErr: context.Canceled, want: nil},
		{name: "real failure", serveErr: boom, want: boom},
		{name: "joined with own stop", serveErr: errors.Join(boom, context.Canceled), want: boom},
		{name: "wrapped own stop", serveErr: fmt.Errorf("lock approval socket: %w", context.Canceled), want: nil},
		{name: "wrapped real failure joined with own stop", serveErr: fmt.Errorf("real: %w", errors.Join(boom, context.Canceled)), want: boom, texts: []string{"real:", "boom"}},
		{name: "wrapped join of own stops", serveErr: fmt.Errorf("serve: %w", errors.Join(context.Canceled, context.Canceled)), want: nil},
		{name: "nested join", serveErr: errors.Join(errors.Join(boom, context.Canceled), context.Canceled), want: boom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			done := make(chan struct{})
			close(done)
			relay := &approvalRelay{
				dir:      dir,
				path:     filepath.Join(dir, approvalSocketName),
				cancel:   func() {},
				done:     done,
				serveErr: tc.serveErr,
			}
			err := relay.close()
			assertCloseResult(t, tc.serveErr, tc.want, tc.texts, err)
			if _, statErr := os.Stat(dir); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("directory survived close: %v", statErr)
			}
		})
	}
}
