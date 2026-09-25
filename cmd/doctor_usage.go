package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/codastre/cli/internal/transcript"
	"github.com/codastre/cli/internal/usage"
)

// usageTrackingDetail describes the local token log and the upload flag, in
// that order: what is recorded, then what is shared. Both are opt-in and
// independent — tracking writes to disk; upload is a separate decision that
// `codastre collect --upload` acts on only when CODASTRE_USAGE_REPORT=1.
func usageTrackingDetail() string {
	state := transcript.LoadState(transcript.StatePath())
	parts := []string{}
	if usage.TrackingEnabled() {
		parts = append(parts, "local log on (CODASTRE_TRACK_TOKENS=1)")
	} else {
		parts = append(parts, "local log off — set CODASTRE_TRACK_TOKENS=1 for `codastre savings`")
	}
	if usage.UploadEnabled() {
		up := "upload on (CODASTRE_USAGE_REPORT=1, via `codastre collect --upload`)"
		if !state.ReportSince.IsZero() {
			up += ", reporting sessions since " + state.ReportSince.Format(time.RFC3339)
		} else {
			up += ", no consent boundary recorded yet"
		}
		parts = append(parts, up)
	} else {
		parts = append(parts, "upload off — nothing leaves this machine")
	}
	if path := usage.DefaultLogPath(); path != "" {
		if info, err := os.Stat(path); err == nil {
			parts = append(parts, fmt.Sprintf("%s (%d KB)", path, info.Size()/1024))
		}
	}
	if n := len(state.Sessions); n > 0 {
		parts = append(parts, fmt.Sprintf("%s collected from transcripts", countLabel(n, "session")))
	} else {
		parts = append(parts, "no transcripts collected — run `codastre collect`")
	}
	// Named last because it is the one source that leaves the machine, and
	// only on an explicit flag.
	parts = append(parts, "server counters on request (`codastre savings --source server`)")
	return strings.Join(parts, "; ")
}

// otelContentVars lists the Claude Code telemetry variables that would export
// prompts, responses or tool content. All five must stay unset.
func otelContentVars() []string {
	var on []string
	for _, name := range []string{
		"OTEL_LOG_USER_PROMPTS",
		"OTEL_LOG_ASSISTANT_RESPONSES",
		"OTEL_LOG_TOOL_DETAILS",
		"OTEL_LOG_TOOL_CONTENT",
		"OTEL_LOG_RAW_API_BODIES",
	} {
		if v := os.Getenv(name); v != "" && v != "0" && !strings.EqualFold(v, "false") {
			on = append(on, name)
		}
	}
	return on
}
