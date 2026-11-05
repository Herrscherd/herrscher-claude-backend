package claude

import (
	"io/fs"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/i18n"
	"github.com/Herrscherd/herrscher-contracts/i18n/i18ntest"
)

func allMessageKeys() []string {
	return []string{
		keySettingCmd, keySettingModel, keySettingStream, keySettingDir, keySettingKind, keySettingEnv,
		keyGatewayPairIncomplete,
		keyModelsRefused, keyModelsPagination, keyModelsTooMany, keyModelsRedirect,
		keyTurnFailed,
	}
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
		got, err := renderText(i18n.English, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != text {
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
		keyGatewayPairIncomplete: "lancement refusé : la session tournerait sur l'abonnement de la machine alors qu'elle passe par la passerelle",
		keyModelsRefused:         "le service de liste des modèles a refusé la requête",
		keyModelsPagination:      "le service de liste des modèles pagine sans curseur",
		keyModelsTooMany:         "le service de liste des modèles a plus de pages que la limite",
		keyModelsRedirect:        "le service de liste des modèles a redirigé hors de son hôte https, la clé n'est pas renvoyée",
		keyTurnFailed:            "claude a signalé une erreur",
	}
	for key, text := range want {
		got, err := renderText(i18n.French, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != text {
			t.Errorf("%s = %q, want %q", key, got, text)
		}
	}
}

func TestEveryKeyIsTranslated(t *testing.T) {
	t.Parallel()
	for _, key := range allMessageKeys() {
		english, err := renderText(i18n.English, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		french, err := renderText(i18n.French, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
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
	}
	for _, tc := range cases {
		want, err := renderText(i18n.English, tc.key)
		if err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if got := tc.err.Error(); got != want {
			t.Errorf("%s: Error() = %q, want %q", tc.key, got, want)
		}
	}
}

func TestSettingHelpComesFromCatalog(t *testing.T) {
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
			if want := englishText(key); setting.Help != want {
				t.Errorf("setting %q help = %q, want %q", setting.Key, setting.Help, want)
			}
		}
	}
	if !found {
		t.Fatal("claude backend is not registered")
	}
}
