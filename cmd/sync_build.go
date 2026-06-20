package cmd

import (
	"encoding/json"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/masking"
)

// parseSyncResult extracts status and job_id from a SYNC response envelope,
// returning "?" for either field that is absent.
func parseSyncResult(payload json.RawMessage) (status, jobID string) {
	var env struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return "?", "?"
	}
	status, jobID = env.Status, env.JobID
	if status == "" {
		status = "?"
	}
	if jobID == "" {
		jobID = "?"
	}
	return status, jobID
}

// buildDiffTuples converts raw git diff entries into the SYNC wire format
// (impl-spec §3.1), masking every path before it leaves the machine.
//
// Renames are decomposed into a delete(old) + add(new) pair. This realizes the
// spec's stated rename semantics — §9.2: "R(old → new): tombstone path_hash(old)
// + add new" — with one path_hash per tuple, and avoids the server's hmac rename
// path which reuses a single path_hash field for both the old-path tombstone and
// the new chunk's payload hash. A pure rename keeps the same blob_sha, so the
// add still hits the embedding cache (zero re-embedding).
//
// For masking_scheme "hmac" each tuple carries the HMAC path_token and the
// path_hash tombstone key the server requires. For "none" only the cleartext
// path_token is sent; the server derives path_hash itself.
func buildDiffTuples(entries []git.DiffEntry, scheme string, key []byte) []map[string]any {
	tuples := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		switch e.Status {
		case "R":
			tuples = append(tuples, deleteTuple(e.OldPath, scheme, key))
			tuples = append(tuples, addTuple(e.Status, e.Path, e.BlobSHA, scheme, key))
		case "D":
			tuples = append(tuples, deleteTuple(e.Path, scheme, key))
		default: // A, M
			tuples = append(tuples, addTuple(e.Status, e.Path, e.BlobSHA, scheme, key))
		}
	}
	return tuples
}

// addTuple builds an add/modify tuple. status is preserved ("A"/"M"); a rename's
// new side is sent as an add.
func addTuple(status, path, blobSHA, scheme string, key []byte) map[string]any {
	if status == "R" {
		status = "A"
	}
	t := map[string]any{
		"status":     status,
		"path_token": pathToken(path, scheme, key),
	}
	if blobSHA != "" {
		t["blob_sha"] = blobSHA
	}
	if scheme == "hmac" {
		t["path_hash"] = masking.PathHash(key, path)
	}
	return t
}

// deleteTuple builds a delete tuple. Deletions carry no blob_sha; for hmac the
// path_hash is the tombstone key the server matches against.
func deleteTuple(path, scheme string, key []byte) map[string]any {
	t := map[string]any{
		"status":     "D",
		"path_token": pathToken(path, scheme, key),
	}
	if scheme == "hmac" {
		t["path_hash"] = masking.PathHash(key, path)
	}
	return t
}

func pathToken(path, scheme string, key []byte) string {
	if scheme == "hmac" {
		return masking.MaskPath(key, path)
	}
	return masking.UnmaskedPathToken(path)
}
