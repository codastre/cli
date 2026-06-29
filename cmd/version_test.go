package cmd

import (
	"runtime/debug"
	"testing"
)

func TestComputeBuildInfo(t *testing.T) {
	cases := []struct {
		name                string
		v, c, d             string
		bi                  *debug.BuildInfo
		wantV, wantC, wantD string
	}{
		{
			name: "ldflags injected release wins over build info",
			v:    "v1.2.3", c: "abcdef0", d: "2026-06-01T00:00:00Z",
			bi:    &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}},
			wantV: "v1.2.3", wantC: "abcdef0", wantD: "2026-06-01T00:00:00Z",
		},
		{
			name: "go install records the module version",
			v:    "dev", c: "none", d: "unknown",
			bi:    &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}},
			wantV: "v1.4.0", wantC: "none", wantD: "unknown",
		},
		{
			name: "local build uses the vcs stamp",
			v:    "dev", c: "none", d: "unknown",
			bi: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc123def4567890abcdef"},
					{Key: "vcs.time", Value: "2026-06-29T10:00:00Z"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			wantV: "abc123def456", wantC: "abc123def4567890abcdef", wantD: "2026-06-29T10:00:00Z",
		},
		{
			name: "dirty local build is marked",
			v:    "dev", c: "none", d: "unknown",
			bi: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc123def4567890"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantV: "abc123def456-dirty", wantC: "abc123def4567890", wantD: "unknown",
		},
		{
			name: "module version with a dirty tree keeps the version and marks dirty",
			v:    "dev", c: "none", d: "unknown",
			bi: &debug.BuildInfo{
				Main: debug.Module{Version: "v2.0.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantV: "v2.0.0-dirty", wantC: "none", wantD: "unknown",
		},
		{
			name: "no build info leaves defaults untouched",
			v:    "dev", c: "none", d: "unknown",
			bi:    nil,
			wantV: "dev", wantC: "none", wantD: "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotV, gotC, gotD := computeBuildInfo(tc.v, tc.c, tc.d, tc.bi)
			if gotV != tc.wantV || gotC != tc.wantC || gotD != tc.wantD {
				t.Fatalf("computeBuildInfo = (%q, %q, %q), want (%q, %q, %q)",
					gotV, gotC, gotD, tc.wantV, tc.wantC, tc.wantD)
			}
		})
	}
}
