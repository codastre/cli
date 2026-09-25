package transcript

import (
	"os"
	"time"
)

// CollectResult reports what one run did. It is the whole user-visible output
// of `codastre collect`: files seen, files that had new bytes, and how much
// was read.
type CollectResult struct {
	FilesSeen    int   `json:"files_seen"`
	FilesParsed  int   `json:"files_parsed"`
	FilesSkipped int   `json:"files_skipped"`
	FilesFailed  int   `json:"files_failed"`
	BytesRead    int64 `json:"bytes_read"`
	Sessions     int   `json:"sessions"`
	Turns        int   `json:"turns"`
}

// Collect parses every transcript under root that has grown since the last
// run and folds it into state. It is incremental and idempotent: a second run
// with nothing appended reads no bytes and changes nothing.
//
// A file that cannot be opened or parsed is counted and skipped. One bad
// transcript must not cost a developer their whole history.
func Collect(root string, state *State, limit int) (CollectResult, error) {
	var res CollectResult
	files, err := Discover(root)
	if err != nil {
		return res, err
	}
	res.FilesSeen = len(files)
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	for _, f := range files {
		mark := state.Files[f.Path]
		if mark == nil {
			mark = &FileMark{}
			state.Files[f.Path] = mark
		}
		// A new inode means a different file at the same path; a smaller size
		// means it was truncated. Either way the offset is meaningless, and
		// the session's counters have to be rebuilt from zero.
		reset := mark.Inode != f.Inode || f.Size < mark.Offset
		if reset {
			if mark.SessionID != "" {
				delete(state.Sessions, mark.SessionID)
			}
			mark.Offset = 0
		}
		if !reset && mark.Offset >= f.Size {
			res.FilesSkipped++
			continue
		}

		inc, next, err := parseFrom(f.Path, mark.Offset)
		if err != nil {
			res.FilesFailed++
			continue
		}
		if next <= mark.Offset {
			// Read, but nothing new was committed: the file ends mid-turn and
			// that turn is replayed whole next time.
			mark.Inode, mark.Size = f.Inode, f.Size
			res.FilesSkipped++
			continue
		}
		res.FilesParsed++
		res.BytesRead += next - mark.Offset

		id := inc.SessionID
		if id == "" {
			id = SessionIDFromPath(f.Path)
			inc.SessionID = id
		}
		inc.Project = f.Project
		sess := state.Sessions[id]
		if sess == nil {
			sess = newSession(id)
			state.Sessions[id] = sess
		}
		sess.Merge(inc)

		mark.Inode = f.Inode
		mark.Size = f.Size
		mark.Offset = next
		mark.SessionID = id
		mark.ParsedAt = time.Now().UTC()
	}

	res.Sessions = len(state.Sessions)
	for _, s := range state.Sessions {
		res.Turns += s.Turns
	}
	return res, nil
}

func parseFrom(path string, offset int64) (*Session, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, 0, err
		}
	}
	return Parse(f, offset)
}
