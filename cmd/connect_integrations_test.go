package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveIntegrationsSourceFallsBackToHosted(t *testing.T) {
	// nil discovery (old server, or unreachable) → the public source.
	if got := resolveIntegrationsSource(nil); got != hostedIntegrations {
		t.Fatalf("nil discovery = %+v, want hosted", got)
	}
	// A server that answers but carries no integrations block behaves the same.
	if got := resolveIntegrationsSource(&serverDiscovery{DashboardURL: "http://d"}); got != hostedIntegrations {
		t.Fatalf("empty integrations = %+v, want hosted", got)
	}
}

func TestResolveIntegrationsSourceRejectsHalfAnswer(t *testing.T) {
	// A source with no marketplace name would print `codastre@` — worse than the
	// hosted default, which at least resolves.
	d := &serverDiscovery{Integrations: integrationsSource{Source: "acme/plugins"}}
	if got := resolveIntegrationsSource(d); got != hostedIntegrations {
		t.Fatalf("half answer = %+v, want hosted", got)
	}
}

func TestResolveIntegrationsSourceUsesServerAnswer(t *testing.T) {
	want := integrationsSource{
		Source:          "https://github.com/acme-private/ai-marketplace.git",
		MarketplaceName: "acme-plugins",
		Label:           "the Acme AI Marketplace",
		URL:             "https://github.com/acme-private/ai-marketplace",
	}
	if got := resolveIntegrationsSource(&serverDiscovery{Integrations: want}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func integrationsDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/system/discovery" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dashboard_url":"http://d","server_base_url":"http://s",
			"version":"1","integrations":{"source":"https://github.com/acme-private/ai-marketplace.git",
			"marketplace_name":"acme-plugins","label":"the Acme AI Marketplace",
			"url":"https://github.com/acme-private/ai-marketplace"}}`))
	}))
}

func hintFor(t *testing.T, target, serverURL string) string {
	t.Helper()
	out := &bytes.Buffer{}
	c := &cobra.Command{}
	c.SetOut(out)
	c.SetContext(context.Background())
	printIntegrationHint(c, target, serverURL)
	return out.String()
}

func TestIntegrationHintUsesTheDeploymentSource(t *testing.T) {
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()

	got := hintFor(t, "claude", srv.URL)
	for _, want := range []string{
		"the Acme AI Marketplace",
		"claude plugin marketplace add https://github.com/acme-private/ai-marketplace.git",
		"claude plugin install codastre@acme-plugins --scope project",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("hint missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, hostedIntegrations.MarketplaceName) {
		t.Fatalf("hosted default leaked into a configured deployment's hint:\n%s", got)
	}
}

func TestIntegrationHintOnlyForHarnessesThatHaveOne(t *testing.T) {
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()

	for _, target := range []string{"codex", "opencode"} {
		if got := hintFor(t, target, srv.URL); got != "" {
			t.Fatalf("%s has no integration yet, but printed:\n%s", target, got)
		}
	}
}

func TestIntegrationHintSurvivesAnUnreachableServer(t *testing.T) {
	// A connect that already succeeded must not fail or hang on the hint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.Close() // closed: connection refused

	got := hintFor(t, "claude", srv.URL)
	if !strings.Contains(got, "codastre@"+hostedIntegrations.MarketplaceName) {
		t.Fatalf("want hosted fallback, got:\n%s", got)
	}
}
