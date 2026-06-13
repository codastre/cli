package masking

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type fixture struct {
	ID            string   `json:"id"`
	MaskingScheme string   `json:"masking_scheme"`
	KeyHex        string   `json:"key_hex"`
	InputPath     string   `json:"input_path"`
	PathToken     string   `json:"path_token"`
	PrefixHashes  []string `json:"prefix_hashes"`
	PathHash      string   `json:"path_hash"`
}

type fixtureFile struct {
	Cases []fixture `json:"cases"`
}

func TestMasking(t *testing.T) {
	data, err := os.ReadFile("../../../schemas/path-masking-fixtures.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f fixtureFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}

	for _, c := range f.Cases {
		t.Run(c.ID, func(t *testing.T) {
			switch c.MaskingScheme {
			case "hmac":
				key, err := hex.DecodeString(c.KeyHex)
				if err != nil {
					t.Fatalf("decode key: %v", err)
				}
				if got := MaskPath(key, c.InputPath); got != c.PathToken {
					t.Errorf("MaskPath = %q, want %q", got, c.PathToken)
				}
				if got := PrefixHashes(key, c.InputPath); !equalSlices(got, c.PrefixHashes) {
					t.Errorf("PrefixHashes = %v, want %v", got, c.PrefixHashes)
				}
				if got := PathHash(key, c.InputPath); got != c.PathHash {
					t.Errorf("PathHash = %q, want %q", got, c.PathHash)
				}
			case "none":
				if got := UnmaskedPathToken(c.InputPath); got != c.PathToken {
					t.Errorf("UnmaskedPathToken = %q, want %q", got, c.PathToken)
				}
				if got := UnmaskedPrefixHashes(c.InputPath); !equalSlices(got, c.PrefixHashes) {
					t.Errorf("UnmaskedPrefixHashes = %v, want %v", got, c.PrefixHashes)
				}
				if got := UnmaskedPathHash(c.InputPath); got != c.PathHash {
					t.Errorf("UnmaskedPathHash = %q, want %q", got, c.PathHash)
				}
			default:
				t.Fatalf("unknown masking_scheme %q", c.MaskingScheme)
			}
		})
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
