// Path masking — per-component HMAC-SHA256 (impl-spec §8.3, D39).
// Bit-precise match to server/chunking/masking.py; validated by shared golden fixtures.
package masking

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// MaskPath returns the path_token for HMAC masking: per-component HMACs joined by "/".
func MaskPath(repoKey []byte, repoRelativePath string) string {
	components := splitPath(repoRelativePath)
	if len(components) == 0 {
		return ""
	}
	tokens := make([]string, len(components))
	for i, c := range components {
		tokens[i] = hmacHex(repoKey, c)
	}
	return strings.Join(tokens, "/")
}

// PrefixHashes returns the cumulative prefix array for HMAC masking.
// prefix_hash[d] = HMAC(key, "/".join(components[0:d+1]))
func PrefixHashes(repoKey []byte, repoRelativePath string) []string {
	components := splitPath(repoRelativePath)
	if len(components) == 0 {
		return nil
	}
	results := make([]string, len(components))
	for d := range components {
		prefix := joinBytes(components[:d+1])
		results[d] = hmacHex(repoKey, []byte(prefix))
	}
	return results
}

// PathHash is the tombstone hash: the last entry of PrefixHashes.
func PathHash(repoKey []byte, repoRelativePath string) string {
	ph := PrefixHashes(repoKey, repoRelativePath)
	if len(ph) == 0 {
		return ""
	}
	return ph[len(ph)-1]
}

// UnmaskedPathToken returns the real NFC-normalized path (masking_scheme = "none").
func UnmaskedPathToken(repoRelativePath string) string {
	return strings.TrimPrefix(norm.NFC.String(repoRelativePath), "/")
}

// UnmaskedPrefixHashes returns SHA-256 prefix hashes for unmasked repos.
func UnmaskedPrefixHashes(repoRelativePath string) []string {
	components := splitPath(repoRelativePath)
	if len(components) == 0 {
		return nil
	}
	results := make([]string, len(components))
	for d := range components {
		prefix := joinBytes(components[:d+1])
		h := sha256.Sum256([]byte(prefix))
		results[d] = hex.EncodeToString(h[:])
	}
	return results
}

// UnmaskedPathHash returns the tombstone hash for unmasked repos (SHA-256 of the full path).
func UnmaskedPathHash(repoRelativePath string) string {
	ph := UnmaskedPrefixHashes(repoRelativePath)
	if len(ph) == 0 {
		return ""
	}
	return ph[len(ph)-1]
}

// splitPath NFC-normalises the path and returns UTF-8-encoded components.
func splitPath(path string) [][]byte {
	normalized := norm.NFC.String(strings.TrimPrefix(path, "/"))
	parts := strings.Split(normalized, "/")
	var components [][]byte
	for _, p := range parts {
		if p != "" {
			components = append(components, []byte(p))
		}
	}
	return components
}

func joinBytes(bs [][]byte) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = string(b)
	}
	return strings.Join(parts, "/")
}

func hmacHex(key, msg []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}
