// Package downloader는 CDN에서 펌웨어 이미지를 로컬 디스크로 스트리밍해 받고,
// 설치기가 쓸 수 있게 하기 전에 크기와 SHA-256 체크섬을 검증한다.
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
	"regexp"
	"strings"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/durablefile"
)

// campaignIDPattern은 로컬 파일 이름에 쓰이는 campaignID가 downloads
// 디렉터리 밖을 가리키지 못하게 막는다.
var campaignIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ErrManifestExpired는 CDN이 서명된 다운로드 URL을 거부했을 때 반환된다
// (DOWNLOAD-REQ-001: HTTP 403 서명 불일치, 410 URL 만료).
// 호출자는 매니페스트를 새로 받아 다운로드를 다시 시도해야 한다. 다운로드
// 실패로 보지 않는다.
var ErrManifestExpired = errors.New("cdn rejected download url: request a new manifest")

// FailureReason은 다운로드 실패의 종류다. 업데이트 결과를 보고하는 호출자
// (예: 에이전트 상태 머신)가 자기 실패 사유로 변환해 쓴다. 이 패키지는 그
// 사유 정의에 직접 의존하지 않는다.
type FailureReason string

const (
	FailureDownloadFailed FailureReason = "DOWNLOAD_FAILED"
	FailureHashMismatch   FailureReason = "HASH_MISMATCH"
)

// Error는 다운로드 실패를 FailureReason과 함께 감싼다.
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

// Result는 다운로드와 검증을 마친 펌웨어 이미지 정보다.
type Result struct {
	Path string
	Size int64
}

// Downloader는 서명된 CDN URL에서 펌웨어 이미지를 DataDir/downloads로 스트리밍해 받는다.
type Downloader struct {
	httpClient *http.Client
	dataDir    string
}

// New는 받은 이미지를 dataDir 아래에 저장하는 Downloader를 만든다.
// httpClient가 nil이면 http.DefaultClient를 쓴다.
func New(dataDir string, httpClient *http.Client) *Downloader {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Downloader{httpClient: httpClient, dataDir: dataDir}
}

// Path는 campaignID의 다운로드가 완료되면 저장되는(또는 저장될) 로컬 경로를
// 반환한다. 재시작 후 복구하는 호출자가 Download가 이미 다 받아 둔 펌웨어를
// 다시 받지 않고 찾을 때 쓴다.
func (d *Downloader) Path(campaignID string) string {
	return filepath.Join(d.dataDir, "downloads", campaignID+".bin")
}

// Download는 downloadURL을 받아 파일 전체를 메모리에 올리지 않고
// <dataDir>/downloads/<campaignID>.bin에 쓴 뒤, 쓴 크기를 fileSize와,
// SHA-256 다이제스트를 expectedSHA256(hex)과 비교해 검증한다.
//
// HTTP 403/410 응답이면 ErrManifestExpired를 반환한다(호출자는 매니페스트를
// 새로 받아 다시 시도한다). 그 밖의 실패(연결 오류, HTTP 404/502/504, 크기
// 불일치, 해시 불일치)는 호출자가 분기할 수 있도록 FailureReason을 담은
// *Error로 반환한다.
func (d *Downloader) Download(ctx context.Context, campaignID, downloadURL string, fileSize int64, expectedSHA256 string) (Result, error) {
	if !campaignIDPattern.MatchString(campaignID) {
		return Result{}, fmt.Errorf("campaign id %q: must contain only letters, digits, or hyphens", campaignID)
	}

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
		// fileSize보다 1바이트만 더 읽는다. 크기 초과는 판정하되 초과분을
		// 전부 디스크에 쓰지 않기 위해서다.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.LimitReader(resp.Body, fileSize+1), resp.Body}
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

	if err := durablefile.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("rename downloaded file: %w", err)
	}

	return Result{Path: path, Size: written}, nil
}

// writeAndHash는 src를 tmpPath에 새 파일로 스트리밍해 쓰면서 SHA-256
// 다이제스트를 계산하고, 쓴 내용을 fsync해 디스크에 반영한다. 메모리에는
// 버퍼 하나 분량만 둔다.
func writeAndHash(tmpPath string, src io.Reader) (written int64, sha256Hex string, err error) {
	file, err := os.Create(tmpPath)
	if err != nil {
		return 0, "", fmt.Errorf("create temp download file: %w", err)
	}

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), src)
	if copyErr != nil {
		file.Close()
		return written, "", copyErr
	}

	if err := durablefile.SyncAndClose(file); err != nil {
		return written, "", err
	}

	return written, hex.EncodeToString(hasher.Sum(nil)), nil
}
