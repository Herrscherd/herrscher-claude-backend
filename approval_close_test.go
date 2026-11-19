package claude

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRelayCloseDropsOnlyItsOwnStop(t *testing.T) {
	t.Parallel()
	boom := errors.New("serve failed")
	cases := []struct {
		name     string
		serveErr error
		want     error
	}{
		{name: "own stop", serveErr: context.Canceled, want: nil},
		{name: "wrapped own stop", serveErr: fmt.Errorf("serve: %w", context.Canceled), want: nil},
		{name: "real failure", serveErr: boom, want: boom},
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
			if tc.want == nil && err != nil {
				t.Fatalf("close = %v, want nil", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("close = %v, want %v", err, tc.want)
			}
			if _, statErr := os.Stat(dir); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("directory survived close: %v", statErr)
			}
		})
	}
}
