package git

import (
	"encoding/json"
	"os"
	"testing"
)

type urlFixtures struct {
	Cases []struct {
		ID        string `json:"id"`
		Input     string `json:"input"`
		Canonical string `json:"canonical"`
	} `json:"cases"`
}

func TestNormalize(t *testing.T) {
	data, err := os.ReadFile("../../../schemas/url-normalization-fixtures.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f urlFixtures
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	for _, c := range f.Cases {
		t.Run(c.ID, func(t *testing.T) {
			got, err := Normalize(c.Input)
			if err != nil {
				t.Fatalf("Normalize(%q): %v", c.Input, err)
			}
			if got != c.Canonical {
				t.Errorf("got %q, want %q", got, c.Canonical)
			}
		})
	}
}
