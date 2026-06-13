// Package mcpshim implements the stdio MCP proxy (impl-spec §2.7).
// It reads newline-delimited JSON-RPC from stdin, forwards each message to the
// server's MCP HTTP endpoint with the auth header, and writes the response to stdout.
// QUERY responses have their path_tokens unmasked and snippets hydrated from disk.
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
			resp = enrichQueryResponse(cfg, resp)
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
