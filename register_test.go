package claude

import (
	"testing"

	"github.com/Akayashuu/herrscher-contracts"
)

func TestSelfRegisteredAsBackend(t *testing.T) {
	for _, p := range contracts.Default.Backends() {
		if p.Manifest.Kind == "claude" {
			if p.Backend == nil {
				t.Fatal("registered claude plugin has a nil backend factory")
			}
			return
		}
	}
	t.Fatal("claude backend did not self-register into contracts.Default")
}
