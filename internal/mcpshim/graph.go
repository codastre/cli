package mcpshim

import "encoding/json"

// enrichGraphResponse unmasks src/dst path_tokens and evidence file_path_tokens
// in GRAPH responses. For each edge:
//   - src["path_token"] -> src["real_path"] (if UnmaskPath returns ok)
//   - dst["path_token"] -> dst["real_path"] (if UnmaskPath returns ok)
//   - evidence["file_path_token"] -> evidence["real_file_path"] (if present and ok)
//
// Each endpoint is unmasked at its OWN repo's mask_key_rev, read from the
// response's repos map ({repo_id: {masking_scheme, mask_key_rev}}). The old
// hardcoded rev 0 silently wrong-unmasked rotated hmac repos (corpus-hygiene
// plan API3); repos absent from the map (or a missing map) still fall back to 0.
func enrichGraphResponse(cfg Config, data []byte) []byte {
	if !cfg.canEnrich() {
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

	// Each element is a graph_edge_result: {edge, src, dst, count, evidence?}
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(edgesRaw, &edges); err != nil {
		return data
	}

	// repo_id -> mask_key_rev from the envelope's repos map.
	repoRevs := map[string]int{}
	if reposRaw, ok := env["repos"]; ok {
		var repos map[string]struct {
			MaskKeyRev int `json:"mask_key_rev"`
		}
		if err := json.Unmarshal(reposRaw, &repos); err == nil {
			for rid, info := range repos {
				repoRevs[rid] = info.MaskKeyRev
			}
		}
	}

	// unmaskNode unmasks node[tokenField] into node[realField] at the node's
	// own repo rev; returns the repo's rev for reuse (evidence rides src's repo).
	unmaskNode := func(e map[string]json.RawMessage, key, tokenField, realField string) int {
		rev := 0
		raw, ok := e[key]
		if !ok {
			return rev
		}
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			return rev
		}
		repoID, _ := unmarshalString(node["repo_id"])
		if r, ok := repoRevs[repoID]; ok {
			rev = r
		}
		if tok, ok := unmarshalString(node[tokenField]); ok {
			// hmac → UnmaskPath; none → identity (token is the real path).
			if realPath, ok := cfg.unmaskOrIdentity(tok, repoID, rev); ok {
				node[realField], _ = json.Marshal(realPath)
				e[key], _ = json.Marshal(node)
			}
		}
		return rev
	}

	for i, e := range edges {
		srcRev := unmaskNode(e, "src", "path_token", "real_path")
		unmaskNode(e, "dst", "path_token", "real_path")

		// Unmask evidence.file_path_token -> evidence.real_file_path (optional
		// field). Evidence is the SRC side's call site, so it unmasks at the src
		// repo's rev and scheme (identity for a `none`-scheme src repo).
		if evRaw, ok := e["evidence"]; ok {
			var ev map[string]json.RawMessage
			if err := json.Unmarshal(evRaw, &ev); err == nil {
				if tok, ok := unmarshalString(ev["file_path_token"]); ok {
					if realPath, ok := cfg.unmaskOrIdentity(tok, edgeNodeRepoID(e, "src"), srcRev); ok {
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
	if repos, ok := backfillRepoURLs(cfg, env["repos"]); ok {
		env["repos"] = repos
	}
	out, _ := json.Marshal(env)
	return out
}

// backfillRepoURLs adds remote_url to each entry of a GRAPH repos map, which the
// server populates with only {masking_scheme, mask_key_rev}. Without it a
// federated edge names its endpoints by bare UUID, so "which service consumes
// this topic?" can't be answered from the response — the agent has to spend extra
// calls, or guess a service from a shared path like app/consumer.py. The proxy
// already holds repo_id → remote_url for hydration, so it fills the gap locally
// and matches QUERY's repos map, where remote_url is present.
//
// Existing keys are never overwritten: if a future server version starts sending
// remote_url, its value wins.
func backfillRepoURLs(cfg Config, reposRaw json.RawMessage) (json.RawMessage, bool) {
	if cfg.RepoRemoteURL == nil || len(reposRaw) == 0 {
		return nil, false
	}
	var repos map[string]map[string]json.RawMessage
	if err := json.Unmarshal(reposRaw, &repos); err != nil || len(repos) == 0 {
		return nil, false
	}
	changed := false
	for repoID, info := range repos {
		if info == nil {
			info = map[string]json.RawMessage{}
		}
		if _, exists := info["remote_url"]; exists {
			continue
		}
		url, ok := cfg.RepoRemoteURL(repoID)
		if !ok || url == "" {
			continue
		}
		info["remote_url"], _ = json.Marshal(url)
		repos[repoID] = info
		changed = true
	}
	if !changed {
		return nil, false
	}
	b, err := json.Marshal(repos)
	if err != nil {
		return nil, false
	}
	return b, true
}

// edgeNodeRepoID returns the repo_id of an edge's "src"/"dst" node, or "" when
// absent. Used to pick the masking scheme for evidence (which rides the src repo).
func edgeNodeRepoID(e map[string]json.RawMessage, key string) string {
	raw, ok := e[key]
	if !ok {
		return ""
	}
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	id, _ := unmarshalString(node["repo_id"])
	return id
}
