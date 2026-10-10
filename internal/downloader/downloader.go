// Package downloader streams firmware images from the CDN to local disk and
// verifies their size and SHA-256 checksum before the file is made available
// to the installer.
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ErrManifestExpired is returned when the CDN rejects the signed download
// URL (DOWNLOAD-REQ-001: HTTP 403 signature mismatch or 410 URL expired).
// The caller must request a fresh manifest and retry the download; this is
// not a download failure.
var ErrManifestExpired = errors.New("cdn rejected download url: request a new manifest")

// FailureReason classifies a download failure. Callers that report update
// results (e.g. the agent's state machine) map this to their own failure
// vocabulary; this package does not depend on that vocabulary directly.
type FailureReason string

const (
	FailureDownloadFailed FailureReason = "DOWNLOAD_FAILED"
	FailureHashMismatch   FailureReason = "HASH_MISMATCH"
)

// Error wraps a download failure together with its FailureReason.
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

// Result describes a successfully downloaded and verified firmware image.
type Result struct {
	Path string
	Size int64
}

// Downloader streams firmware images from signed CDN URLs to DataDir/downloads.
type Downloader struct {
	httpClient *http.Client
	dataDir    string
}

// New creates a Downloader that stores downloaded images under dataDir.
// If httpClient is nil, http.DefaultClient is used.
func New(dataDir string, httpClient *http.Client) *Downloader {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Downloader{httpClient: httpClient, dataDir: dataDir}
}

// Path returns the local path a completed download for campaignID is (or
// will be) written to. Callers recovering from a restart use this to locate
// a firmware image that Download already finished writing, without
// re-downloading it.
func (d *Downloader) Path(campaignID string) string {
	return filepath.Join(d.dataDir, "downloads", campaignID+".bin")
}

// Download fetches downloadURL and writes it to
// <dataDir>/downloads/<campaignID>.bin without buffering the whole file in
// memory, then verifies the written size against fileSize and its SHA-256
// digest against expectedSHA256 (hex-encoded).
//
// It returns ErrManifestExpired for HTTP 403/410 responses (caller should
// request a new manifest and retry). All other failures — connection
// errors, HTTP 404/502/504, size mismatch, or hash mismatch — are returned
// as *Error with a FailureReason the caller can branch on.
func (d *Downloader) Download(ctx context.Context, campaignID, downloadURL string, fileSize int64, expectedSHA256 string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("build download request: %w", err)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return Result{}, &Error{Reason: FailureDownloadFailed, Err: fmt.Errorf("call cdn: %w", err)}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// continue below
	case http.StatusForbidden, http.StatusGone:
		return Result{}, ErrManifestExpired
	default:
		return Result{}, &Error{Reason: FailureDownloadFailed, Err: fmt.Errorf("cdn returned http %d", resp.StatusCode)}
	}

	path := d.Path(campaignID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return Result{}, fmt.Errorf("create downloads dir: %w", err)
	}

	tmpPath := path + ".tmp"

	written, actualSHA256, err := writeAndHash(tmpPath, resp.Body)
	if err != nil {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureDownloadFailed, Err: fmt.Errorf("stream download body: %w", err)}
	}

	if written != fileSize {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureDownloadFailed, Err: fmt.Errorf("downloaded size %d does not match expected %d", written, fileSize)}
	}

	if !strings.EqualFold(actualSHA256, expectedSHA256) {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureHashMismatch, Err: fmt.Errorf("sha256 %s does not match expected %s", actualSHA256, expectedSHA256)}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("rename downloaded file: %w", err)
	}

	return Result{Path: path, Size: written}, nil
}

// writeAndHash streams src to a new file at tmpPath while computing its
// SHA-256 digest, keeping only one buffer's worth of data in memory.
func writeAndHash(tmpPath string, src io.Reader) (written int64, sha256Hex string, err error) {
	file, err := os.Create(tmpPath)
	if err != nil {
		return 0, "", fmt.Errorf("create temp download file: %w", err)
	}

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), src)
	closeErr := file.Close()

	if copyErr != nil {
		return written, "", copyErr
	}
	if closeErr != nil {
		return written, "", fmt.Errorf("close temp download file: %w", closeErr)
	}

	return written, hex.EncodeToString(hasher.Sum(nil)), nil
}
