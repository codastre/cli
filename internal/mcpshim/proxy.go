// Package mcpshim implements the stdio MCP proxy (impl-spec §2.7).
// It reads newline-delimited JSON-RPC from stdin, forwards each message to the
// server's MCP HTTP endpoint with the auth header, and writes the response to stdout.
// QUERY responses have their path_tokens unmasked and snippets hydrated from disk.
// GRAPH responses have their src/dst path_tokens and evidence file_path_tokens unmasked.
package mcpshim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Config configures the MCP proxy.
type Config struct {
	ServerURL string
	APIKey    string
	RepoRoot  string
	// UnmaskPath, if non-nil, converts a path_token to the real path for a given mask_key_rev.
	UnmaskPath func(pathToken string, maskKeyRev int) (string, bool)
}

// Run reads JSON-RPC messages from in, proxies them to the server, and writes
// responses to out. Blocks until in is closed or a read error occurs.
func Run(cfg Config, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, err := forwardMessage(cfg, line)
		if err != nil {
			resp = errorEnvelope(line, err)
		} else {
			resp = enrichResponse(cfg, resp)
		}
		fmt.Fprintf(out, "%s\n", resp)
	}
	return sc.Err()
}

func forwardMessage(cfg Config, body []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, data)
	}
	return data, nil
}

// enrichResponse dispatches to enrichQueryResponse or enrichGraphResponse based
// on which top-level key is present in the JSON payload. Returns data unchanged
// if neither "results" nor "edges" is found.
func enrichResponse(cfg Config, data []byte) []byte {
	if cfg.UnmaskPath == nil {
		return data
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	if _, ok := env["results"]; ok {
		return enrichQueryResponse(cfg, data)
	}
	if _, ok := env["edges"]; ok {
		return enrichGraphResponse(cfg, data)
	}
	return data
}

// enrichQueryResponse unmasks path_tokens and hydrates snippets in QUERY responses.
func enrichQueryResponse(cfg Config, data []byte) []byte {
	if cfg.UnmaskPath == nil || cfg.RepoRoot == "" {
		return data
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	resultsRaw, ok := env["results"]
	if !ok {
		return data
	}

	var results []map[string]json.RawMessage
	if err := json.Unmarshal(resultsRaw, &results); err != nil {
		return data
	}

	var maskKeyRev int
	if raw, ok := env["mask_key_rev"]; ok {
		_ = json.Unmarshal(raw, &maskKeyRev)
	}

	for i, r := range results {
		var pathToken string
		if raw, ok := r["path_token"]; ok {
			_ = json.Unmarshal(raw, &pathToken)
		}
		realPath, ok := cfg.UnmaskPath(pathToken, maskKeyRev)
		if !ok {
			continue
		}
		r["real_path"], _ = json.Marshal(realPath)

		// Snippet hydration (impl-spec §2.7).
		var lineStart, lineEnd int
		var blobSHA string
		if raw, ok := r["line_start"]; ok {
			_ = json.Unmarshal(raw, &lineStart)
		}
		if raw, ok := r["line_end"]; ok {
			_ = json.Unmarshal(raw, &lineEnd)
		}
		if raw, ok := r["blob_sha"]; ok {
			_ = json.Unmarshal(raw, &blobSHA)
		}

		absPath := cfg.RepoRoot + "/" + realPath
		snippet, stale, err := hydrateSnippet(absPath, lineStart, lineEnd, blobSHA)
		if err == nil {
			r["snippet"], _ = json.Marshal(snippet)
			if stale {
				r["stale"], _ = json.Marshal(true)
			}
		}
		results[i] = r
	}

	enriched, _ := json.Marshal(results)
	env["results"] = enriched
	out, _ := json.Marshal(env)
	return out
}

// enrichGraphResponse unmasks src/dst path_tokens and evidence file_path_tokens
// in GRAPH responses. For each edge:
//   - src["path_token"] -> src["real_path"] (if UnmaskPath returns ok)
//   - dst["path_token"] -> dst["real_path"] (if UnmaskPath returns ok)
//   - evidence["file_path_token"] -> evidence["real_file_path"] (if present and ok)
//
// Uses maskKeyRev=0 since GRAPH responses carry no top-level mask_key_rev field.
func enrichGraphResponse(cfg Config, data []byte) []byte {
	if cfg.UnmaskPath == nil {
		return data
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return data
	}
	edgesRaw, ok := env["edges"]
	if !ok {
		return data
	}

	// Each element is a graph_edge_result: {edge, src, dst, confidence, resolution, evidence?}
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(edgesRaw, &edges); err != nil {
		return data
	}

	const maskKeyRev = 0

	for i, e := range edges {
		// Unmask src.path_token -> src.real_path
		if srcRaw, ok := e["src"]; ok {
			var src map[string]json.RawMessage
			if err := json.Unmarshal(srcRaw, &src); err == nil {
				if tok, ok := unmarshalString(src["path_token"]); ok {
					if realPath, ok := cfg.UnmaskPath(tok, maskKeyRev); ok {
						src["real_path"], _ = json.Marshal(realPath)
						e["src"], _ = json.Marshal(src)
					}
				}
			}
		}

		// Unmask dst.path_token -> dst.real_path
		if dstRaw, ok := e["dst"]; ok {
			var dst map[string]json.RawMessage
			if err := json.Unmarshal(dstRaw, &dst); err == nil {
				if tok, ok := unmarshalString(dst["path_token"]); ok {
					if realPath, ok := cfg.UnmaskPath(tok, maskKeyRev); ok {
						dst["real_path"], _ = json.Marshal(realPath)
						e["dst"], _ = json.Marshal(dst)
					}
				}
			}
		}

		// Unmask evidence.file_path_token -> evidence.real_file_path (optional field)
		if evRaw, ok := e["evidence"]; ok {
			var ev map[string]json.RawMessage
			if err := json.Unmarshal(evRaw, &ev); err == nil {
				if tok, ok := unmarshalString(ev["file_path_token"]); ok {
					if realPath, ok := cfg.UnmaskPath(tok, maskKeyRev); ok {
						ev["real_file_path"], _ = json.Marshal(realPath)
						e["evidence"], _ = json.Marshal(ev)
					}
				}
			}
		}

		edges[i] = e
	}

	enriched, _ := json.Marshal(edges)
	env["edges"] = enriched
	out, _ := json.Marshal(env)
	return out
}

// unmarshalString extracts a Go string from a json.RawMessage.
// Returns ("", false) if raw is nil or not a JSON string.
func unmarshalString(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// hydrateSnippet reads [lineStart, lineEnd] from the file and checks staleness.
func hydrateSnippet(absPath string, lineStart, lineEnd int, blobSHA string) (string, bool, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var lines []string
	lineNum := 1
	for sc.Scan() {
		if lineNum >= lineStart && lineNum <= lineEnd {
			lines = append(lines, sc.Text())
		}
		if lineNum > lineEnd {
			break
		}
		lineNum++
	}
	if err := sc.Err(); err != nil {
		return "", false, err
	}

	snippet := strings.Join(lines, "\n")

	// Staleness check: compare current blob hash to expected.
	stale := false
	if blobSHA != "" {
		current, err := currentBlobSHA(absPath)
		if err == nil && current != blobSHA {
			stale = true
		}
	}

	return snippet, stale, nil
}

func currentBlobSHA(absPath string) (string, error) {
	out, err := exec.Command("git", "hash-object", absPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// errorEnvelope extracts the JSON-RPC id from req and wraps err as a JSON-RPC error.
func errorEnvelope(req []byte, err error) []byte {
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(req, &msg)
	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      msg.ID,
		"error":   map[string]any{"code": -32000, "message": err.Error()},
	}
	b, _ := json.Marshal(env)
	return b
}
