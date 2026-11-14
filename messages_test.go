package claude

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
	"github.com/Herrscherd/herrscher-contracts/i18n/i18ntest"
)

const nonBreakingSpace = " "

func allMessageKeys() []string {
	return []string{
		keySettingCmd, keySettingModel, keySettingStream, keySettingDir, keySettingKind, keySettingEnv,
		keyGatewayPairIncomplete,
		keyModelsRefused, keyModelsPagination, keyModelsTooMany, keyModelsRedirect,
		keyTurnFailed, keyTurnStreamEnded, keyTurnResponderClosed,
		keyUsageNoLogin, keyUsageExpired, keyUsageRefused, keyUsageUnknown, keyUsageLoginFile,
	}
}

func mustRender(t *testing.T, lang i18n.Language, key string) string {
	t.Helper()
	text, err := newMessages().render(lang, key)
	if err != nil {
		t.Fatalf("%s in %s: %v", key, lang, err)
	}
	return text
}

func catalogIDs(t *testing.T, file string) []string {
	t.Helper()
	raw, err := fs.ReadFile(localeFiles, "locales/"+file)
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func TestCatalogDeclaresEveryKeyInEveryLanguage(t *testing.T) {
	t.Parallel()
	sources, err := fs.Sub(localeFiles, "locales")
	if err != nil {
		t.Fatal(err)
	}
	declared := make(map[string][]string)
	for _, key := range allMessageKeys() {
		declared[key] = nil
	}
	i18ntest.CheckDeclared(t, declared, sources)
}

func sourceMessageKeys(t *testing.T) map[string]bool {
	t.Helper()
	keys := make(map[string]bool)
	for _, decl := range parseMessagesSource(t).Decls {
		for _, key := range keyConstValues(t, decl) {
			keys[key] = true
		}
	}
	return keys
}

func parseMessagesSource(t *testing.T) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "messages.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func keyConstValues(t *testing.T, decl ast.Decl) []string {
	t.Helper()
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.CONST {
		return nil
	}
	var values []string
	for _, spec := range gen.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range valueSpec.Names {
			if strings.HasPrefix(name.Name, "key") && i < len(valueSpec.Values) {
				values = append(values, unquoteConst(t, name.Name, valueSpec.Values[i]))
			}
		}
	}
	return values
}

func unquoteConst(t *testing.T, name string, expr ast.Expr) string {
	t.Helper()
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		t.Fatalf("%s is not a string literal", name)
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func diffKeySets(t *testing.T, label string, want, got map[string]bool) {
	t.Helper()
	for key := range want {
		if !got[key] {
			t.Errorf("%s lacks %q", label, key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("%s has %q, which the source does not declare", label, key)
		}
	}
}

func TestMessageKeysListAllSourceKeys(t *testing.T) {
	t.Parallel()
	listed := make(map[string]bool)
	for _, key := range allMessageKeys() {
		listed[key] = true
	}
	diffKeySets(t, "allMessageKeys", sourceMessageKeys(t), listed)
}

func TestMessageKeysMatchEachCatalogFile(t *testing.T) {
	t.Parallel()
	want := sourceMessageKeys(t)
	for _, file := range []string{"en.json", "fr.json"} {
		got := make(map[string]bool)
		for _, id := range catalogIDs(t, file) {
			got[id] = true
		}
		diffKeySets(t, file, want, got)
	}
}

func TestEmbeddedCatalogLoads(t *testing.T) {
	t.Parallel()
	messages := newMessages()
	for _, lang := range []i18n.Language{i18n.English, i18n.French} {
		for _, key := range allMessageKeys() {
			if _, err := messages.render(lang, key); err != nil {
				t.Fatalf("%s in %s: %v", key, lang, err)
			}
		}
	}
}

func TestEnglishMessages(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		keySettingCmd:            "base command to run the agent",
		keySettingModel:          "model override",
		keySettingStream:         "stream-json mode (false to disable)",
		keySettingDir:            "working directory",
		keySettingKind:           "backend kind",
		keySettingEnv:            "per-session env injected at spawn (K=V per line); host-supplied, never from the environment",
		keyGatewayPairIncomplete: "refusing to spawn: the session would run on the machine's own subscription while being routed through the gateway",
		keyModelsRefused:         "models endpoint refused the request",
		keyModelsPagination:      "models endpoint paginates without a cursor",
		keyModelsTooMany:         "models endpoint has more pages than the limit",
		keyModelsRedirect:        "models endpoint redirected away from its own https host, the key is not resent",
		keyTurnFailed:            "claude reported an error",
	}
	for key, text := range want {
		if got := mustRender(t, i18n.English, key); got != text {
			t.Errorf("%s = %q, want %q", key, got, text)
		}
	}
}

func TestFrenchMessages(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		keySettingCmd:            "commande de base qui lance l'agent",
		keySettingModel:          "remplacement du modèle",
		keySettingStream:         "mode stream-json (false pour le désactiver)",
		keySettingDir:            "dossier de travail",
		keySettingKind:           "type de backend",
		keySettingEnv:            "environnement propre à la session, injecté au lancement (K=V par ligne)" + nonBreakingSpace + "; fourni par l'hôte, jamais lu depuis l'environnement",
		keyGatewayPairIncomplete: "lancement refusé" + nonBreakingSpace + ": la session tournerait sur l'abonnement de la machine alors qu'elle passe par la passerelle",
		keyModelsRefused:         "le service de liste des modèles a refusé la requête",
		keyModelsPagination:      "le service de liste des modèles annonce une page suivante sans curseur",
		keyModelsTooMany:         "le service de liste des modèles a plus de pages que la limite",
		keyModelsRedirect:        "le service de liste des modèles a redirigé hors de son hôte https, la clé n'est pas renvoyée",
		keyTurnFailed:            "claude a signalé une erreur",
	}
	for key, text := range want {
		if got := mustRender(t, i18n.French, key); got != text {
			t.Errorf("%s = %q, want %q", key, got, text)
		}
	}
}

func TestFrenchPunctuationUsesNonBreakingSpace(t *testing.T) {
	t.Parallel()
	for _, key := range allMessageKeys() {
		text := mustRender(t, i18n.French, key)
		for _, mark := range []string{":", ";", "?", "!"} {
			for idx := strings.Index(text, mark); idx >= 0; {
				if !strings.HasSuffix(text[:idx], nonBreakingSpace) {
					t.Errorf("%s: %q is not preceded by a non-breaking space in %q", key, mark, text)
				}
				next := strings.Index(text[idx+len(mark):], mark)
				if next < 0 {
					break
				}
				idx += len(mark) + next
			}
		}
	}
}

func TestEveryKeyIsTranslated(t *testing.T) {
	t.Parallel()
	for _, key := range allMessageKeys() {
		english := mustRender(t, i18n.English, key)
		french := mustRender(t, i18n.French, key)
		if english == french {
			t.Errorf("%s is not translated: %q", key, french)
		}
	}
}

func TestErrorsCarryTheCatalogEnglish(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err error
		key string
	}{
		{ErrGatewayPairIncomplete, keyGatewayPairIncomplete},
		{errModelsStatus, keyModelsRefused},
		{errModelsPagination, keyModelsPagination},
		{errModelsTooMany, keyModelsTooMany},
		{errModelsRedirect, keyModelsRedirect},
		{errTurnReported, keyTurnFailed},
	}
	for _, tc := range cases {
		if want := mustRender(t, i18n.English, tc.key); tc.err.Error() != want {
			t.Errorf("%s: Error() = %q, want %q", tc.key, tc.err.Error(), want)
		}
	}
}

func TestSettingHelpEqualsEnglishCatalog(t *testing.T) {
	t.Parallel()
	keys := map[string]string{
		"cmd":    keySettingCmd,
		"model":  keySettingModel,
		"stream": keySettingStream,
		"dir":    keySettingDir,
		"kind":   keySettingKind,
		"env":    keySettingEnv,
	}
	var found bool
	for _, plugin := range contracts.Default.Backends() {
		if plugin.Manifest.Kind != backendKind {
			continue
		}
		found = true
		for _, setting := range plugin.Manifest.Config {
			key, ok := keys[setting.Key]
			if !ok {
				t.Fatalf("setting %q has no catalog key", setting.Key)
			}
			if want := mustRender(t, i18n.English, key); setting.Help != want {
				t.Errorf("setting %q help = %q, want %q", setting.Key, setting.Help, want)
			}
		}
	}
	if !found {
		t.Fatal("claude backend is not registered")
	}
}
