package claude

import (
	"embed"
	"fmt"
	"io/fs"
	"sync"

	"github.com/Herrscherd/herrscher-contracts/i18n"
)

//go:embed locales
var localeFiles embed.FS

const (
	keySettingCmd            = "setting.cmd"
	keySettingModel          = "setting.model"
	keySettingStream         = "setting.stream"
	keySettingDir            = "setting.dir"
	keySettingKind           = "setting.kind"
	keySettingEnv            = "setting.env"
	keyGatewayPairIncomplete = "gateway.pair_incomplete"
	keyApprovalUnreachable   = "approval.unreachable"
	keyApprovalNoTurn        = "approval.no_turn"
	keyApprovalMismatch      = "approval.mismatch"
	keyApprovalOpenFailed    = "approval.open_failed"
	keyApprovalPathTooLong   = "approval.path_too_long"
	keyApprovalDirFailed     = "approval.dir_failed"
	keyModelsRefused         = "models.refused"
	keyModelsPagination      = "models.pagination"
	keyModelsTooMany         = "models.too_many_pages"
	keyModelsRedirect        = "models.redirect"
	keyTurnFailed            = "turn.failed"
	keyTurnStreamEnded       = "turn.stream_ended"
	keyTurnResponderClosed   = "turn.responder_closed"
	keyUsageNoLogin          = "usage.no_oauth_login"
	keyUsageExpired          = "usage.token_expired"
	keyUsageRefused          = "usage.refused"
	keyUsageUnknown          = "usage.unknown"
	keyUsageLoginFile        = "usage.login_file_malformed"
)

type messages struct {
	load func() (*i18n.Catalog, error)
}

func newMessages() *messages {
	return &messages{load: sync.OnceValues(loadCatalog)}
}

func loadCatalog() (*i18n.Catalog, error) {
	sources, err := fs.Sub(localeFiles, "locales")
	if err != nil {
		return nil, fmt.Errorf("claude locales: %w", err)
	}
	return i18n.NewCatalog(nil, sources)
}

func (m *messages) renderWith(lang i18n.Language, key string, params map[string]any) (string, error) {
	catalog, err := m.load()
	if err != nil {
		return "", err
	}
	return catalog.Message(lang, key, params)
}

func (m *messages) localized(lang i18n.Language, key string) string {
	return m.localizedWith(lang, key, nil)
}

func (m *messages) localizedWith(lang i18n.Language, key string, params map[string]any) string {
	text, err := m.renderWith(lang, key, params)
	if err != nil {
		return m.englishWith(key, params)
	}
	return text
}

func (m *messages) english(key string) string {
	return m.englishWith(key, nil)
}

func (m *messages) englishWith(key string, params map[string]any) string {
	text, err := m.renderWith(i18n.English, key, params)
	if err != nil {
		return fmt.Sprintf("message %s unavailable: %v", key, err)
	}
	return text
}

type catalogError struct {
	text  string
	cause error
}

func newCatalogError(lang i18n.Language, key string, cause error) error {
	return newCatalogErrorWith(lang, key, nil, cause)
}

func newCatalogErrorWith(lang i18n.Language, key string, params map[string]any, cause error) error {
	return &catalogError{text: newMessages().localizedWith(lang, key, params), cause: cause}
}

func (e *catalogError) Error() string {
	return e.text
}

func (e *catalogError) Unwrap() error {
	return e.cause
}
