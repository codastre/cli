package report

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codastre/cli/internal/transcript"
)

// Distinctive strings in every field the uploader must never send.
const (
	secretSessionID = "SECRET-SESSION-ID-0001"
	secretCwd       = "/SECRET/CWD/path"
	secretProject   = "-SECRET-PROJECT"
	secretBranch    = "SECRET_BRANCH"
)

var keyHex = strings.Repeat("ab", 32)

func t0() time.Time { return time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC) }

func fixtureSession(id string, start time.Time, withCost bool) *transcript.Session {
	s := &transcript.Session{
		SessionID:     id,
		Project:       secretProject,
		Cwd:           secretCwd,
		GitBranch:     secretBranch,
		ClientVersion: "2.1.0",
		Source:        transcript.SourceTranscript,
		StartedAt:     start,
		EndedAt:       start.Add(10 * time.Minute),
		Turns:         3,
		ToolMix: map[string]*transcript.ToolStat{
			"mcp__codastre__QUERY": {Calls: 2, ResultBytes: 900, Errors: 1},
			"Read":                 {Calls: 1, ResultBytes: 50},
			"bad tool name!":       {Calls: 1, ResultBytes: 7},
		},
		EpisodeLog: []transcript.Episode{
			{Ordinal: 0, Outcome: transcript.OutcomeCodastreOnly, StartedAt: start, EndedAt: start.Add(time.Minute),
				CodastreCalls: 1, CodastreBytes: 400},
			{Ordinal: 2, Outcome: transcript.OutcomeFallbackAfter, StartedAt: start.Add(2 * time.Minute),
				EndedAt: start.Add(3 * time.Minute), CodastreCalls: 1, CodastreBytes: 500, ReadCalls: 1, ReadBytes: 50},
		},
	}
	if withCost {
		s.Cost = transcript.CostState{Present: true, CostUSD: 0.5, DurationMS: 600000, InputTokens: 10,
			CacheReadTokens: 900, ToolDurationMS: 1000, LinesAdded: 2}
	}
	return s
}

// The allowlists are the wire contract (§3/§4). A new key here is a new field
// on the wire and must be a deliberate, reviewed change.
var (
	episodeKeys = []string{
		"basis_mix", "client_version", "codastre_bytes", "codastre_calls", "codastre_tokens",
		"ended_at", "episode_ordinal", "mode", "outcome", "plane_mix", "read_bytes", "read_calls",
		"read_tokens", "session_ref", "source", "started_at", "text_search_bytes",
		"text_search_calls", "text_search_tokens",
	}
	sessionKeys = []string{
		"cache_creation_tokens", "cache_read_tokens", "client_version", "compactions_auto",
		"compactions_manual", "cost_usd", "duration_ms", "ended_at", "input_tokens", "lines_added",
		"lines_removed", "output_tokens", "repo_id", "session_ref", "source", "started_at",
		"thinking_tokens", "tool_duration_ms", "tool_mix",
	}
)

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	blob, _ := json.Marshal(v)
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRowsMatchTheWireAllowlist(t *testing.T) {
	s := fixtureSession(secretSessionID, t0(), true)
	ref := SessionRef([]byte("k"), s.SessionID)
	eps := EpisodeRows(s, ref, 0)
	if len(eps) != 2 {
		t.Fatalf("episodes = %d", len(eps))
	}
	if got := keysOf(t, eps[0]); strings.Join(got, ",") != strings.Join(episodeKeys, ",") {
		t.Errorf("episode keys = %v", got)
	}
	row, ok := BuildSessionRow(s, ref, &transcript.Compactions{Auto: 2, Manual: 1})
	if !ok {
		t.Fatal("session row not built")
	}
	if got := keysOf(t, row); strings.Join(got, ",") != strings.Join(sessionKeys, ",") {
		t.Errorf("session keys = %v", got)
	}
	if row.CompactionsAuto != 2 || row.CompactionsManual != 1 {
		t.Errorf("compactions = %d/%d", row.CompactionsAuto, row.CompactionsManual)
	}
	if _, ok := row.ToolMix["bad tool name!"]; ok || row.ToolMix["other"].Calls != 1 {
		t.Errorf("tool mix did not fold an off-pattern name into other: %+v", row.ToolMix)
	}
	// Transcript rows: exact bytes, no token estimate, mixes absent (null).
	blob, _ := json.Marshal(eps[0])
	for _, want := range []string{`"plane_mix":null`, `"basis_mix":null`, `"mode":"unknown"`,
		`"source":"transcript"`, `"codastre_tokens":0`, `"codastre_bytes":400`} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("episode JSON missing %s: %s", want, blob)
		}
	}
}

func TestSessionWithoutCostStateIsNotARow(t *testing.T) {
	if _, ok := BuildSessionRow(fixtureSession("x", t0(), false), "r", nil); ok {
		t.Error("a session with no cost-state must not produce a row (cost is never modelled)")
	}
}

func TestSessionRefIsHMACSHA256Hex(t *testing.T) {
	// RFC 4231 test case 2.
	got := SessionRef([]byte("Jefe"), "what do ya want for nothing?")
	want := "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
	if got != want {
		t.Errorf("SessionRef = %s, want %s", got, want)
	}
}

type stub struct {
	srv      *httptest.Server
	hits     map[string]int
	bodies   []string
	episodes int
}

func newStub(t *testing.T) *stub {
	t.Helper()
	st := &stub{hits: map[string]int{}}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.hits[r.Method+" "+r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/me/usage/session-key":
			_, _ = io.WriteString(w, `{"key":"`+keyHex+`","algorithm":"hmac-sha256","kind":"usage_session_key"}`)
		case "/v1/usage/episodes", "/v1/usage/sessions":
			st.bodies = append(st.bodies, string(body))
			var p map[string][]json.RawMessage
			_ = json.Unmarshal(body, &p)
			n := len(p["episodes"]) + len(p["sessions"])
			if r.URL.Path == "/v1/usage/episodes" {
				st.episodes += n
			}
			_ = json.NewEncoder(w).Encode(Ack{Received: n, Inserted: n})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func stateWith(sessions ...*transcript.Session) *transcript.State {
	st := transcript.LoadState("")
	for _, s := range sessions {
		st.Sessions[s.SessionID] = s
	}
	return st
}

func TestRunRecordsConsentAndSendsOnlyNewSessions(t *testing.T) {
	st := newStub(t)
	c := &Client{BaseURL: st.srv.URL, APIKey: "k"}
	now := t0()
	old := fixtureSession("old-"+secretSessionID, now.Add(-time.Hour), true) // before consent
	state := stateWith(old)

	res, err := Run(context.Background(), c, state, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ConsentRecorded || !state.ReportSince.Equal(now) {
		t.Errorf("consent not recorded: %+v", res)
	}
	if res.SessionsBeforeConsent != 1 || len(st.hits) != 0 {
		t.Errorf("pre-consent history was sent: res=%+v hits=%v", res, st.hits)
	}

	fresh := fixtureSession(secretSessionID, now.Add(time.Minute), true)
	state.Sessions[fresh.SessionID] = fresh
	state.Compactions[fresh.SessionID] = &transcript.Compactions{Auto: 1}
	res, err = Run(context.Background(), c, state, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.ConsentRecorded || !state.ReportSince.Equal(now) {
		t.Errorf("consent boundary moved: %+v", state.ReportSince)
	}
	if res.Episodes.Received != 2 || res.Sessions.Received != 1 {
		t.Errorf("sent = %+v", res)
	}
	if fresh.Upload.EpisodesThrough != 3 || fresh.Upload.SessionSig == "" {
		t.Errorf("upload marks = %+v", fresh.Upload)
	}
	all := strings.Join(st.bodies, "\n")
	for _, secret := range []string{secretSessionID, secretCwd, secretProject, secretBranch} {
		if strings.Contains(all, secret) {
			t.Errorf("upload body leaked %q", secret)
		}
	}
	if !strings.Contains(all, SessionRef([]byte(strings.Repeat("\xab", 32)), fresh.SessionID)) {
		t.Error("upload body does not carry the HMAC session_ref")
	}

	// Nothing new → no request at all, not even the key fetch.
	before := len(st.hits)
	for k := range st.hits {
		st.hits[k] = 0
	}
	if _, err := Run(context.Background(), c, state, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for k, n := range st.hits {
		if n != 0 {
			t.Errorf("no-op run hit %s (%d, of %d paths)", k, n, before)
		}
	}

	// The session grew (a compaction and a new episode): only the new episode
	// and the changed session row are re-sent.
	fresh.EpisodeLog = append(fresh.EpisodeLog, transcript.Episode{Ordinal: 3,
		Outcome: transcript.OutcomeTextSearchOnly, StartedAt: now.Add(5 * time.Minute),
		EndedAt: now.Add(6 * time.Minute), TextSearchCalls: 1, TextSearchBytes: 10})
	state.Compactions[fresh.SessionID].Auto = 2
	res, err = Run(context.Background(), c, state, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Episodes.Received != 1 || res.Sessions.Received != 1 {
		t.Errorf("re-send = %+v", res)
	}
}

func TestRunBatchesAtTheServerLimit(t *testing.T) {
	st := newStub(t)
	c := &Client{BaseURL: st.srv.URL, APIKey: "k"}
	now := t0()
	s := fixtureSession("big", now.Add(time.Minute), false)
	s.EpisodeLog = nil
	for i := 0; i < MaxBatch+7; i++ {
		s.EpisodeLog = append(s.EpisodeLog, transcript.Episode{Ordinal: i,
			Outcome: transcript.OutcomeCodastreOnly, StartedAt: now.Add(time.Minute), CodastreCalls: 1})
	}
	state := stateWith(s)
	state.ReportSince = now
	res, err := Run(context.Background(), c, state, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.hits["POST /v1/usage/episodes"] != 2 || res.Episodes.Received != MaxBatch+7 {
		t.Errorf("batches = %d, received = %d", st.hits["POST /v1/usage/episodes"], res.Episodes.Received)
	}
	if res.SessionsWithoutCost != 1 || st.hits["POST /v1/usage/sessions"] != 0 {
		t.Errorf("a cost-less session was sent: %+v", res)
	}
}

func TestRunSurfacesAuthErrors(t *testing.T) {
	st := newStub(t)
	c := &Client{BaseURL: st.srv.URL, APIKey: "wrong"}
	state := stateWith(fixtureSession("s", t0().Add(time.Minute), true))
	state.ReportSince = t0()
	_, err := Run(context.Background(), c, state, t0())
	if err == nil || !strings.Contains(err.Error(), "codastre login") {
		t.Errorf("err = %v", err)
	}
	if state.Sessions["s"].Upload.EpisodesThrough != 0 {
		t.Error("marks advanced on a failed upload")
	}
}
