package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const contractsEnvelopeJSON = `{
  "status": "ok",
  "scope": {
    "visible_repos": 4,
    "repos_with_endpoints": 1,
    "endpoints_read": 812,
    "cross_repo_possible": false,
    "warnings": ["endpoints_in_one_repo"]
  },
  "counts": {"matched": 3, "internal": 2, "orphan_exposer": 5,
             "orphan_user": 1, "quarantined": 4},
  "total": 15,
  "contracts": [
    {"contract_id": "topic::kafka::orders", "kind": "kafka", "name": "orders",
     "method": null, "status": "orphan_user",
     "exposers": [],
     "users": [{"repo_id": "11111111-2222-3333-4444-555555555555",
                "chunk_id": "c2", "path_token": "svc/consumer.go", "line": 12,
                "role": "consumer", "confidence": 0.71,
                "path_class": null, "host": null, "quarantined": false}]},
    {"contract_id": "http::GET::/users/{}", "kind": "http", "name": "/users/{}",
     "method": "GET", "status": "orphan_exposer",
     "exposers": [{"repo_id": "11111111-2222-3333-4444-555555555555",
                   "chunk_id": "c1", "path_token": "api/handlers/users.go", "line": 42,
                   "role": "route", "confidence": 0.95,
                   "path_class": "test", "host": null, "quarantined": true}],
     "users": []}
  ],
  "repos": {"11111111-2222-3333-4444-555555555555":
            {"masking_scheme": "hmac", "mask_key_rev": 3}}
}`

func renderContractsToString(t *testing.T, body string, unmask func(string, int) (string, bool), agent bool) string {
	t.Helper()
	var out bytes.Buffer
	if err := renderContracts(&out, json.RawMessage(body), unmask, agent); err != nil {
		t.Fatalf("renderContracts: %v", err)
	}
	return out.String()
}

// The whole reason the scope block exists: an orphan-heavy answer over a
// too-narrow scope looks identical to a real finding. The caveat is worthless
// if the reader meets it after the list they have already believed.
func TestRenderContracts_WarningsPrecedeTheList(t *testing.T) {
	for _, agent := range []bool{false, true} {
		got := renderContractsToString(t, contractsEnvelopeJSON, nil, agent)
		warnAt := strings.Index(got, "warning:")
		listAt := strings.Index(got, "topic::kafka::orders")
		if warnAt < 0 {
			t.Fatalf("agent=%v: no warning rendered:\n%s", agent, got)
		}
		if listAt < 0 || warnAt > listAt {
			t.Fatalf("agent=%v: warning must precede the list:\n%s", agent, got)
		}
		if !strings.HasPrefix(got, "scope:") {
			t.Fatalf("agent=%v: output must lead with the scope line:\n%s", agent, got)
		}
	}
}

// "cross_repo_possible: false" is the difference between "nothing is wired up"
// and "nothing could have been". It cannot be left to the reader to infer.
func TestRenderContracts_ScopeStatesReachability(t *testing.T) {
	got := renderContractsToString(t, contractsEnvelopeJSON, nil, false)
	if !strings.Contains(got, "cross-repo matches NOT reachable") {
		t.Fatalf("unreachable scope not stated:\n%s", got)
	}
	if !strings.Contains(got, "4 repo(s) visible") || !strings.Contains(got, "812 endpoint(s) read") {
		t.Fatalf("scope numbers missing:\n%s", got)
	}
}

// The counts cover every assembled contract, not the filtered list — the
// orphan totals are only meaningful read against matched/internal.
func TestRenderContracts_CountsCoverEveryStatus(t *testing.T) {
	got := renderContractsToString(t, contractsEnvelopeJSON, nil, false)
	for _, want := range []string{
		"orphan_exposer 5", "orphan_user 1", "matched 3", "internal 2", "quarantined 4",
		"total 15, showing 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in counts line:\n%s", want, got)
		}
	}
}

func TestRenderContracts_HumanGroupsByStatusAndPrintsParties(t *testing.T) {
	got := renderContractsToString(t, contractsEnvelopeJSON, nil, false)
	// orphan_exposer sorts before orphan_user: the default report leads with the
	// side the caller owns.
	expAt := strings.Index(got, "orphan_exposer — exposed, nothing indexed uses it")
	userAt := strings.Index(got, "orphan_user — used, nothing indexed exposes it")
	if expAt < 0 || userAt < 0 || expAt > userAt {
		t.Fatalf("status grouping/order wrong:\n%s", got)
	}
	if !strings.Contains(got, "api/handlers/users.go:42") ||
		!strings.Contains(got, "(route, 0.95)") {
		t.Fatalf("party location/role missing:\n%s", got)
	}
	// The empty side is the finding; an omitted "none" reads as truncation.
	if !strings.Contains(got, "users: none") || !strings.Contains(got, "exposers: none") {
		t.Fatalf("empty side not stated:\n%s", got)
	}
}

// Quarantined parties are shown with a tag, never dropped — mirroring the
// REST/tool contract. Dropping one turns a real orphan into a silent nothing.
func TestRenderContracts_QuarantinedPartyIsTaggedNotDropped(t *testing.T) {
	got := renderContractsToString(t, contractsEnvelopeJSON, nil, false)
	if !strings.Contains(got, "[quarantined]") {
		t.Fatalf("quarantined tag missing:\n%s", got)
	}
}

func TestRenderContracts_UnmasksPartyPathsAtTheRepoKeyRev(t *testing.T) {
	unmask := func(token string, rev int) (string, bool) {
		if token == "api/handlers/users.go" && rev == 3 {
			return "real/users.go", true
		}
		return "", false
	}
	got := renderContractsToString(t, contractsEnvelopeJSON, unmask, false)
	if !strings.Contains(got, "real/users.go:42") {
		t.Fatalf("path not unmasked at the repo's own key rev:\n%s", got)
	}
	// A token the checkout can't resolve stays masked rather than vanishing.
	if !strings.Contains(got, "svc/consumer.go:12") {
		t.Fatalf("unresolved token must fall back to the raw path_token:\n%s", got)
	}
}

// Party lines carry a short repo id; the legend is where the full UUID (the
// value --repo takes) and the masking scheme live.
func TestRenderContracts_RepoLegendExpandsShortIDs(t *testing.T) {
	got := renderContractsToString(t, contractsEnvelopeJSON, nil, false)
	if !strings.Contains(got, "11111111  11111111-2222-3333-4444-555555555555  (masking hmac, key rev 3)") {
		t.Fatalf("repo legend missing or malformed:\n%s", got)
	}
}

func TestRenderContracts_AgentIsOneLinePerContract(t *testing.T) {
	got := renderContractsToString(t, contractsEnvelopeJSON, nil, true)
	for _, want := range []string{
		"orphan_exposer http::GET::/users/{} — exposers: 1 (11111111) · users: 0",
		"orphan_user topic::kafka::orders — exposers: 0 · users: 1 (11111111)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing agent line %q:\n%s", want, got)
		}
	}
	// No party detail in agent format — the line is scanned, not read.
	if strings.Contains(got, "api/handlers/users.go") {
		t.Fatalf("agent format must not print party locations:\n%s", got)
	}
}

func TestRenderContracts_EmptyIsNotAnError(t *testing.T) {
	body := `{"status":"ok","total":9,
	  "scope":{"visible_repos":1,"repos_with_endpoints":1,"endpoints_read":40,
	           "cross_repo_possible":false,"warnings":["single_repo_scope"]},
	  "counts":{"matched":0,"internal":9,"orphan_exposer":0,"orphan_user":0,"quarantined":0},
	  "contracts":[],"repos":{}}`
	got := renderContractsToString(t, body, nil, false)
	if !strings.Contains(got, "no contracts matched this filter (9 assembled in scope)") {
		t.Fatalf("empty answer must report the assembled total:\n%s", got)
	}
	// A zero explained by scope must not read as a clean bill of health.
	if !strings.Contains(got, "only one repo is in scope") {
		t.Fatalf("scope warning must survive the empty path:\n%s", got)
	}
}

// A newer server may add a status, a kind or a warning. None of them may
// disappear from the report just because this binary predates them.
func TestRenderContracts_UnknownValuesStillRender(t *testing.T) {
	body := `{"status":"ok","total":1,
	  "scope":{"visible_repos":2,"repos_with_endpoints":2,"endpoints_read":5,
	           "cross_repo_possible":true,"warnings":["some_new_warning"]},
	  "counts":{"matched":0,"internal":0,"orphan_exposer":0,"orphan_user":0,
	            "quarantined":0,"disputed":1},
	  "contracts":[{"contract_id":"grpc::Svc/Method","kind":"grpc","name":"Svc/Method",
	                "method":null,"status":"disputed","exposers":[],"users":[]}],
	  "repos":{}}`
	got := renderContractsToString(t, body, nil, false)
	for _, want := range []string{"some_new_warning", "disputed 1", "grpc", "grpc::Svc/Method"} {
		if !strings.Contains(got, want) {
			t.Fatalf("unknown value %q dropped:\n%s", want, got)
		}
	}
}

func TestDecodeContracts_RejectsGarbage(t *testing.T) {
	if _, ok := decodeContracts(json.RawMessage(`not json`)); ok {
		t.Fatal("decode accepted non-JSON")
	}
}

// ── flags → tool arguments ──────────────────────────────────────────────────

func TestContractsToolArgs_OmitsUnsetFilters(t *testing.T) {
	defer resetContractsFlags()
	resetContractsFlags()
	// No filters set: the server's own default (the orphan report) must apply,
	// so nothing may be sent — an empty list would ask for nothing at all.
	if got := contractsToolArgs(); len(got) != 0 {
		t.Fatalf("unset filters must send no arguments, got %v", got)
	}
}

func TestContractsToolArgs_ForwardsEveryFilter(t *testing.T) {
	defer resetContractsFlags()
	contractsStatus = []string{"matched", "internal"}
	contractsKind = []string{"kafka"}
	contractsRepo = []string{"11111111-2222-3333-4444-555555555555"}

	got := contractsToolArgs()
	assertStringSlice(t, got, "status", []string{"matched", "internal"})
	assertStringSlice(t, got, "kind", []string{"kafka"})
	assertStringSlice(t, got, "repo", []string{"11111111-2222-3333-4444-555555555555"})
}

func assertStringSlice(t *testing.T, args map[string]any, key string, want []string) {
	t.Helper()
	got, ok := args[key].([]string)
	if !ok {
		t.Fatalf("%s: missing or not a []string in %v", key, args)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", key, got, want)
	}
}

func resetContractsFlags() {
	contractsStatus, contractsKind, contractsRepo = nil, nil, nil
	contractsJSON, contractsNoUnmask = false, false
	contractsFormat, contractsKey, contractsRepoPath = "human", "", ""
}

// ── end to end, against a fake MCP server ───────────────────────────────────

// runContractsAgainst executes the command end to end against a stub MCP
// endpoint, returning stdout and the arguments the tool was called with.
func runContractsAgainst(t *testing.T, args []string) (string, map[string]any) {
	t.Helper()
	var toolArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Params.Name != "CONTRACTS" {
			t.Errorf("tool = %q, want CONTRACTS", req.Params.Name)
		}
		toolArgs = req.Params.Arguments
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": contractsEnvelopeJSON}},
			},
		})
	}))
	defer srv.Close()

	defer resetContractsFlags()
	resetContractsFlags()
	var out bytes.Buffer
	// Dispatch through the root command: cobra's Execute always runs from the
	// root, so driving the subcommand directly would just print root's help.
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	}()
	// --key short-circuits the keychain; --no-unmask keeps the run offline.
	rootCmd.SetArgs(append([]string{"contracts", "--server", srv.URL, "--key", "k", "--no-unmask"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out.String(), toolArgs
}

func TestRunContracts_FiltersReachTheTool(t *testing.T) {
	_, args := runContractsAgainst(t, []string{
		"--status", "matched", "--kind", "http", "--kind", "kafka",
		"--repo", "11111111-2222-3333-4444-555555555555",
	})
	// Arguments arrive as JSON, so slices come back as []any.
	for key, want := range map[string][]string{
		"status": {"matched"},
		"kind":   {"http", "kafka"},
		"repo":   {"11111111-2222-3333-4444-555555555555"},
	} {
		raw, ok := args[key].([]any)
		if !ok || len(raw) != len(want) {
			t.Fatalf("%s = %#v, want %v", key, args[key], want)
		}
		for i, v := range raw {
			if v != want[i] {
				t.Fatalf("%s[%d] = %v, want %v", key, i, v, want[i])
			}
		}
	}
}

func TestRunContracts_DefaultSendsNoFilters(t *testing.T) {
	_, args := runContractsAgainst(t, nil)
	if len(args) != 0 {
		t.Fatalf("default run must send no filters, got %#v", args)
	}
}

func TestRunContracts_ThreeFormats(t *testing.T) {
	human, _ := runContractsAgainst(t, nil)
	if !strings.Contains(human, "exposers (1):") {
		t.Fatalf("human format missing party detail:\n%s", human)
	}

	agent, _ := runContractsAgainst(t, []string{"--format", "agent"})
	if !strings.Contains(agent, "orphan_exposer http::GET::/users/{} — exposers: 1") {
		t.Fatalf("agent format wrong:\n%s", agent)
	}

	raw, _ := runContractsAgainst(t, []string{"--json"})
	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("--json is not a raw envelope: %v\n%s", err, raw)
	}
	if env["total"].(float64) != 15 {
		t.Fatalf("--json envelope altered: %v", env["total"])
	}
}

func TestRunContracts_NoUnmaskLeavesTokensRaw(t *testing.T) {
	got, _ := runContractsAgainst(t, nil) // runs with --no-unmask
	if !strings.Contains(got, "api/handlers/users.go:42") {
		t.Fatalf("--no-unmask must print the raw path_token:\n%s", got)
	}
}
