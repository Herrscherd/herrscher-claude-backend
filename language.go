package claude

import (
	"strings"
	"unicode"

	"github.com/Herrscherd/herrscher-contracts/i18n"
)

const (
	frenchDirective  = "Reply in French."
	englishDirective = "Reply in English."
)

func languageDirective(lang i18n.Language) string {
	switch lang {
	case i18n.French:
		return frenchDirective
	case i18n.English:
		return englishDirective
	}
	return ""
}

func withLanguage(lang i18n.Language, text string) string {
	directive := languageDirective(lang)
	if directive == "" {
		return text
	}
	return directive + "\n\n" + text
}

func isSlashCommand(text string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), "/")
}

func localizedTurnFailure(lang i18n.Language) string {
	return newMessages().localized(lang, keyTurnFailed)
}
