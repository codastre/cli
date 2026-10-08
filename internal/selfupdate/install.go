package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// ExtractBinary pulls the codastre executable out of a release archive
// (tar.gz everywhere but Windows, zip there — see .goreleaser.yaml).
func ExtractBinary(archiveName string, data []byte) ([]byte, error) {
	want := "codastre"
	if strings.HasSuffix(archiveName, ".zip") {
		return extractZip(data, want+".exe")
	}
	return extractTarGz(data, want)
}

func extractTarGz(data []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("archive has no %s", want)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == want {
			return readCapped(tr)
		}
	}
}

func extractZip(data []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().Mode().IsRegular() && path.Base(f.Name) == want {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("read archive: %w", err)
			}
			defer rc.Close()
			return readCapped(rc)
		}
	}
	return nil, fmt.Errorf("archive has no %s", want)
}

func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read binary: %w", err)
	}
	if len(data) > maxArchiveBytes {
		return nil, errors.New("binary exceeds size limit")
	}
	return data, nil
}

// ExecutablePath resolves the running binary through symlinks, so the
// replacement lands on the real file rather than a link to it.
func ExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Replace swaps the file at target for binary. The new bytes go to a temp file
// in the same directory and are renamed over target, so target is never left
// half-written, and a process already running the old binary (e.g. `codastre
// serve`) keeps its inode and runs on untouched until it restarts.
//
// Windows cannot rename over a running executable, so there the old file is
// first moved aside to "<target>.old" (removed on the next update).
func Replace(target string, binary []byte) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".codastre-update-*")
	if err != nil {
		return writeErr(dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return writeErr(dir, err)
	}
	if err := tmp.Close(); err != nil {
		return writeErr(dir, err)
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()|0o111); err != nil {
		return writeErr(dir, err)
	}

	if runtime.GOOS == "windows" {
		old := target + ".old"
		_ = os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			return writeErr(dir, err)
		}
		if err := os.Rename(tmpName, target); err != nil {
			_ = os.Rename(old, target)
			return writeErr(dir, err)
		}
		return nil
	}
	if err := os.Rename(tmpName, target); err != nil {
		return writeErr(dir, err)
	}
	return nil
}

func writeErr(dir string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("no write permission in %s — re-run with sudo, or reinstall into a user-writable directory: %w", dir, err)
	}
	return err
}
