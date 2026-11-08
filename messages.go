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
	keyModelsRefused         = "models.refused"
	keyModelsPagination      = "models.pagination"
	keyModelsTooMany         = "models.too_many_pages"
	keyModelsRedirect        = "models.redirect"
	keyTurnFailed            = "turn.failed"
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

func (m *messages) render(lang i18n.Language, key string) (string, error) {
	catalog, err := m.load()
	if err != nil {
		return "", err
	}
	return catalog.Message(lang, key, nil)
}

func (m *messages) localized(lang i18n.Language, key string) string {
	text, err := m.render(lang, key)
	if err != nil {
		return m.english(key)
	}
	return text
}

func (m *messages) english(key string) string {
	text, err := m.render(i18n.English, key)
	if err != nil {
		return fmt.Sprintf("message %s unavailable: %v", key, err)
	}
	return text
}

type catalogError struct {
	text string
}

func newCatalogError(key string) error {
	return &catalogError{text: newMessages().english(key)}
}

func (e *catalogError) Error() string {
	return e.text
}
