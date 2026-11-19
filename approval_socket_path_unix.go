//go:build unix && !linux

package claude

const (
	approvalSocketPathLimit = 104
	approvalFallbackDir     = "/tmp"
)
