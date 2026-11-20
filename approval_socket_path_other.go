//go:build !unix

package claude

const (
	approvalSocketPathLimit = 108
	approvalFallbackDir     = ""
)
