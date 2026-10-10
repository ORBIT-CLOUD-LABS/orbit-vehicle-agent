// Package installer reflects a verified firmware file into the vehicle's
// single install location.
package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FailureReason classifies an installation failure. Callers that report
// update results (e.g. the agent's state machine) map this to their own
// failure vocabulary; this package does not depend on that vocabulary
// directly.
type FailureReason string

const FailureInstallFailed FailureReason = "INSTALL_FAILED"

// Error wraps an installation failure together with its FailureReason.
type Error struct {
	Reason FailureReason
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Result describes a successfully installed firmware image.
type Result struct {
	Path string
}

// Installer reflects verified firmware into DataDir/firmware/current.bin.
type Installer struct {
	dataDir string
}

// New creates an Installer that installs into dataDir.
func New(dataDir string) *Installer {
	return &Installer{dataDir: dataDir}
}

// Install copies the verified firmware at sourcePath into
// DataDir/firmware/current.bin, replacing any existing file atomically via
// a temp file and os.Rename.
//
// Install is idempotent: it keeps no in-memory record of prior installs, so
// calling it again with the same sourcePath content — including after a
// process restart — simply overwrites current.bin with the same bytes and
// returns the same Result.
func (i *Installer) Install(ctx context.Context, sourcePath string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	src, err := os.Open(sourcePath)
	if err != nil {
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("open verified firmware: %w", err)}
	}
	defer src.Close()

	dir := filepath.Join(i.dataDir, "firmware")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Result{}, fmt.Errorf("create firmware dir: %w", err)
	}

	path := filepath.Join(dir, "current.bin")
	tmpPath := path + ".tmp"

	dst, err := os.Create(tmpPath)
	if err != nil {
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("create temp install file: %w", err)}
	}

	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()

	if copyErr != nil {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("copy firmware to install location: %w", copyErr)}
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("close temp install file: %w", closeErr)}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("rename installed file: %w", err)
	}

	return Result{Path: path}, nil
}
