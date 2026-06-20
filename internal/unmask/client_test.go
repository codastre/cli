package unmask

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveRepoPaginatesAndMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(repoPage{
				Items: []RepoInfo{
					{RepoID: "a", RemoteURL: "github.com/acme/one", MaskingScheme: "none"},
				},
				NextCursor: strptr("page2"),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(repoPage{
			Items: []RepoInfo{
				{RepoID: "b", RemoteURL: "github.com/acme/two", MaskingScheme: "hmac", MaskKeyRev: 3},
			},
		})
	}))
	defer srv.Close()

	info, err := ResolveRepo(srv.URL, "tok", "github.com/acme/two")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.RepoID != "b" || !info.IsMasked() || info.MaskKeyRev != 3 {
		t.Fatalf("got %+v, want repo b hmac rev3", info)
	}
}

func TestResolveRepoNoMatchReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(repoPage{Items: []RepoInfo{{RepoID: "a", RemoteURL: "x"}}})
	}))
	defer srv.Close()

	info, err := ResolveRepo(srv.URL, "tok", "github.com/acme/missing")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatalf("expected nil for no match, got %+v", info)
	}
}

func TestFetchMaskingKeysDecodesHex(t *testing.T) {
	k1 := strings.Repeat("aa", 32)
	k2 := strings.Repeat("bb", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/masking-key") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(maskingKeyResponse{Revs: map[string]string{"1": k1, "2": k2}})
	}))
	defer srv.Close()

	keys, err := FetchMaskingKeys(srv.URL, "tok", "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	want1, _ := hex.DecodeString(k1)
	want2, _ := hex.DecodeString(k2)
	if string(keys[1]) != string(want1) || string(keys[2]) != string(want2) {
		t.Fatalf("decoded keys mismatch: %x / %x", keys[1], keys[2])
	}
}

func TestResolveBaseIndexPrefersDefaultBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]IndexInfo{
			{IndexID: "feat", BaseRefName: "feature-x", BaseRefSHA: "f1", Status: "ready"},
			{IndexID: "main", BaseRefName: "main", BaseRefSHA: "m1", Status: "ready"},
			{IndexID: "bld", BaseRefName: "master", BaseRefSHA: "x1", Status: "building"},
		})
	}))
	defer srv.Close()

	idx, err := ResolveBaseIndex(srv.URL, "tok", "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if idx == nil || idx.IndexID != "main" {
		t.Fatalf("want main index, got %+v", idx)
	}
}

func TestResolveBaseIndexNoReadyReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]IndexInfo{
			{IndexID: "bld", BaseRefName: "main", Status: "building"},
		})
	}))
	defer srv.Close()

	idx, err := ResolveBaseIndex(srv.URL, "tok", "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if idx != nil {
		t.Fatalf("expected nil when no ready index, got %+v", idx)
	}
}

func TestFetchMaskingKeysHandles204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	keys, err := FetchMaskingKeys(srv.URL, "tok", "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected empty map on 204, got %v", keys)
	}
}

func strptr(s string) *string { return &s }
