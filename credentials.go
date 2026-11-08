package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	credentialsFileName     = ".credentials.json"
	nativeConfigDirName     = ".claude"
	credentialsMaxBodyBytes = 1 << 20
)

var (
	errNoOAuthLogin = newCatalogError(keyUsageNoLogin)
	errOAuthExpired = newCatalogError(keyUsageExpired)
)

type credentialsFile struct {
	ClaudeAiOauth *oauthCredentials `json:"claudeAiOauth"`
}

type oauthCredentials struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   int64  `json:"expiresAt"`
}

func probeGetenv(access contracts.ProbeAccess) func(string) string {
	if access.Getenv == nil {
		return os.Getenv
	}
	return access.Getenv
}

func accountConfigDir(getenv func(string) string) string {
	if dir := strings.TrimSpace(getenv(contracts.EnvClaudeConfigDir)); dir != "" {
		return dir
	}
	home := strings.TrimSpace(getenv("HOME"))
	if home == "" {
		return ""
	}
	return filepath.Join(home, nativeConfigDirName)
}

func readOAuthToken(getenv func(string) string, now time.Time) (string, error) {
	dir := accountConfigDir(getenv)
	if dir == "" {
		return "", fmt.Errorf("%w: %w", contracts.ErrLimitsUnavailable, errNoOAuthLogin)
	}
	credentials, err := readCredentials(filepath.Join(dir, credentialsFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %w", contracts.ErrLimitsUnavailable, errNoOAuthLogin)
	}
	if err != nil {
		return "", limitsUnavailable(err)
	}
	oauth := credentials.ClaudeAiOauth
	if oauth == nil || strings.TrimSpace(oauth.AccessToken) == "" {
		return "", fmt.Errorf("%w: %w", contracts.ErrLimitsUnavailable, errNoOAuthLogin)
	}
	if oauth.ExpiresAt > 0 && !now.Before(time.UnixMilli(oauth.ExpiresAt)) {
		return "", fmt.Errorf("%w: %w", contracts.ErrLimitsUnavailable, errOAuthExpired)
	}
	return strings.TrimSpace(oauth.AccessToken), nil
}

func readCredentials(path string) (credentialsFile, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return credentialsFile{}, fmt.Errorf("open claude credentials directory: %w", err)
	}
	defer closeAndLog(root)
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return credentialsFile{}, fmt.Errorf("open claude credentials: %w", err)
	}
	defer closeAndLog(file)
	var credentials credentialsFile
	if err := json.NewDecoder(io.LimitReader(file, credentialsMaxBodyBytes)).Decode(&credentials); err != nil {
		return credentialsFile{}, fmt.Errorf("decode claude credentials: %w", err)
	}
	return credentials, nil
}
