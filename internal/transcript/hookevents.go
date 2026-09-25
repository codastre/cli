package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// The hook event log is written by the plugin's session_events.js hook
// (PreCompact / PostCompact / PostToolUseFailure) — the signals a transcript
// does not record and that cannot be recovered after the fact. Each line is
// counters and enums only; see docs/plans/m3-plane3-wire-contract.md §1.

// HookLogPath is where the hook appends: $CODASTRE_SESSION_EVENTS_LOG, or
// ~/.config/codastre/session-events.jsonl.
func HookLogPath() string {
	if p := os.Getenv("CODASTRE_SESSION_EVENTS_LOG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "session-events.jsonl")
}

// hookEvent declares only the fields that become counters.
type hookEvent struct {
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	Phase     string `json:"phase"`
	Trigger   string `json:"trigger"`
}

// HookResult reports what one pass over the hook log did.
type HookResult struct {
	BytesRead   int64 `json:"bytes_read"`
	Compactions int   `json:"compactions"`
}

// CollectHookEvents folds new hook-log lines into state.Compactions, resuming
// from state.HookLog. When the log was rotated (new inode) the remainder of
// the rotated `.1` file is read first, so events written between the last run
// and the rotation are not lost. A missing log is not an error: most machines
// have no hook installed.
func CollectHookEvents(path string, state *State) (HookResult, error) {
	var res HookResult
	if path == "" {
		return res, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, err
	}
	if state.Compactions == nil {
		state.Compactions = map[string]*Compactions{}
	}
	mark := state.HookLog
	if mark == nil {
		mark = &FileMark{}
		state.HookLog = mark
	}
	inode := inodeOf(info)
	if mark.Inode != 0 && mark.Inode != inode {
		if rinfo, err := os.Stat(path + ".1"); err == nil && inodeOf(rinfo) == mark.Inode {
			n, c, _ := readHookFrom(path+".1", mark.Offset, state)
			res.BytesRead += n
			res.Compactions += c
		}
		mark.Offset = 0
	}
	if info.Size() < mark.Offset {
		mark.Offset = 0 // truncated in place
	}
	n, c, err := readHookFrom(path, mark.Offset, state)
	if err != nil {
		return res, err
	}
	res.BytesRead += n
	res.Compactions += c
	mark.Inode = inode
	mark.Size = info.Size()
	mark.Offset += n
	mark.ParsedAt = time.Now().UTC()
	return res, nil
}

// readHookFrom reads complete lines from offset and returns the bytes it
// committed (never past the last newline) and the compactions it counted.
func readHookFrom(path string, offset int64, state *State) (int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, 0, err
		}
	}
	var read int64
	counted := 0
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			read += int64(len(line))
			if applyHookLine(line, state) {
				counted++
			}
		}
		if err != nil {
			if err == io.EOF {
				return read, counted, nil
			}
			return read, counted, err
		}
	}
}

// applyHookLine counts one compaction. Only the PreCompact phase counts —
// counting Post as well would double every compaction — and an unknown
// trigger is dropped rather than guessed into either bucket.
func applyHookLine(line []byte, state *State) bool {
	var ev hookEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return false // malformed line; never fatal
	}
	if ev.Event != "compact" || ev.Phase != "pre" || ev.SessionID == "" {
		return false
	}
	c := state.Compactions[ev.SessionID]
	if c == nil {
		c = &Compactions{}
	}
	switch ev.Trigger {
	case "auto":
		c.Auto++
	case "manual":
		c.Manual++
	default:
		return false
	}
	state.Compactions[ev.SessionID] = c
	return true
}
