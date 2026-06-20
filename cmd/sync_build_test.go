package cmd

import (
	"encoding/json"
	"testing"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/masking"
)

var syncKey = []byte("0123456789abcdef0123456789abcdef")

func TestBuildDiffTuples_HmacAddModify(t *testing.T) {
	entries := []git.DiffEntry{
		{Status: "A", Path: "src/new.go", BlobSHA: "aaa"},
		{Status: "M", Path: "src/old.go", BlobSHA: "bbb"},
	}
	got := buildDiffTuples(entries, "hmac", syncKey)
	if len(got) != 2 {
		t.Fatalf("want 2 tuples, got %d", len(got))
	}

	if got[0]["status"] != "A" || got[0]["blob_sha"] != "aaa" {
		t.Errorf("add tuple wrong: %+v", got[0])
	}
	if got[0]["path_token"] != masking.MaskPath(syncKey, "src/new.go") {
		t.Errorf("path_token not HMAC-masked: %v", got[0]["path_token"])
	}
	if got[0]["path_hash"] != masking.PathHash(syncKey, "src/new.go") {
		t.Errorf("path_hash mismatch: %v", got[0]["path_hash"])
	}
}

func TestBuildDiffTuples_HmacDelete(t *testing.T) {
	got := buildDiffTuples([]git.DiffEntry{{Status: "D", Path: "gone.go", BlobSHA: "ccc"}}, "hmac", syncKey)
	if len(got) != 1 {
		t.Fatalf("want 1 tuple, got %d", len(got))
	}
	d := got[0]
	if d["status"] != "D" {
		t.Errorf("want D, got %v", d["status"])
	}
	if _, ok := d["blob_sha"]; ok {
		t.Errorf("delete tuple must not carry blob_sha: %+v", d)
	}
	if d["path_hash"] != masking.PathHash(syncKey, "gone.go") {
		t.Errorf("delete path_hash mismatch: %v", d["path_hash"])
	}
}

func TestBuildDiffTuples_RenameDecomposes(t *testing.T) {
	entries := []git.DiffEntry{
		{Status: "R", OldPath: "a/old.go", Path: "b/new.go", BlobSHA: "ddd"},
	}
	got := buildDiffTuples(entries, "hmac", syncKey)
	if len(got) != 2 {
		t.Fatalf("rename must decompose into 2 tuples, got %d: %+v", len(got), got)
	}

	// First tuple: delete(old) — tombstones the old path hash, no blob_sha.
	del := got[0]
	if del["status"] != "D" {
		t.Errorf("first tuple should be D, got %v", del["status"])
	}
	if del["path_token"] != masking.MaskPath(syncKey, "a/old.go") {
		t.Errorf("delete should mask old path: %v", del["path_token"])
	}
	if del["path_hash"] != masking.PathHash(syncKey, "a/old.go") {
		t.Errorf("delete tombstone must be old path hash: %v", del["path_hash"])
	}
	if _, ok := del["blob_sha"]; ok {
		t.Errorf("delete must not carry blob_sha")
	}

	// Second tuple: add(new) — new path hash + same blob_sha (cache hit).
	add := got[1]
	if add["status"] != "A" {
		t.Errorf("second tuple should be A (rename's new side), got %v", add["status"])
	}
	if add["path_token"] != masking.MaskPath(syncKey, "b/new.go") {
		t.Errorf("add should mask new path: %v", add["path_token"])
	}
	if add["path_hash"] != masking.PathHash(syncKey, "b/new.go") {
		t.Errorf("add path_hash must be new path hash, not old: %v", add["path_hash"])
	}
	if add["blob_sha"] != "ddd" {
		t.Errorf("rename add must keep blob_sha for cache hit: %v", add["blob_sha"])
	}
}

func TestBuildDiffTuples_NoneSchemeOmitsPathHash(t *testing.T) {
	got := buildDiffTuples([]git.DiffEntry{{Status: "A", Path: "x/y.go", BlobSHA: "eee"}}, "none", nil)
	tok := got[0]["path_token"]
	if tok != masking.UnmaskedPathToken("x/y.go") {
		t.Errorf("none scheme should send cleartext token: %v", tok)
	}
	if _, ok := got[0]["path_hash"]; ok {
		t.Errorf("none scheme must omit path_hash (server derives it): %+v", got[0])
	}
}

func TestParseSyncResult(t *testing.T) {
	payload := json.RawMessage(`{"status":"ok","job_id":"abc-123"}`)
	status, jobID := parseSyncResult(payload)
	if status != "ok" || jobID != "abc-123" {
		t.Fatalf("got (%q,%q), want (ok, abc-123)", status, jobID)
	}

	status, jobID = parseSyncResult(json.RawMessage(`{}`))
	if status != "?" || jobID != "?" {
		t.Fatalf("missing fields should yield (?,?), got (%q,%q)", status, jobID)
	}
}
