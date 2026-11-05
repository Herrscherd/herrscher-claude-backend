package claude

import (
	"embed"
	"fmt"
	"io/fs"

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
)

func newCatalog() (*i18n.Catalog, error) {
	sources, err := fs.Sub(localeFiles, "locales")
	if err != nil {
		return nil, fmt.Errorf("claude locales: %w", err)
	}
	return i18n.NewCatalog(nil, sources)
}

func renderText(lang i18n.Language, key string) (string, error) {
	catalog, err := newCatalog()
	if err != nil {
		return "", err
	}
	return catalog.Message(lang, key, nil)
}

func englishText(key string) string {
	text, err := renderText(i18n.English, key)
	if err != nil {
		return key
	}
	return text
}

type catalogError struct {
	text string
}

func newCatalogError(key string) error {
	return &catalogError{text: englishText(key)}
}

func (e *catalogError) Error() string {
	return e.text
}
