package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

const (
	edgeRetryLimit    = 2
	edgeRetryInterval = 250 * time.Millisecond
)

// errAbortManifestNotTarget과 errAbortManifestError는 downloadWithManifestRetry가
// 매니페스트 재요청 결과를 에러 반환값으로 알리게 해 준다.
var (
	errAbortManifestNotTarget = errors.New("manifest refresh: no longer an update target")
	errAbortManifestError     = errors.New("manifest refresh failed")
	errAllEdgesFailed         = errors.New("all download edges failed")
	errDownloadURLsExpired    = errors.New("download URLs expired")
)

type edgeDownloadResult struct {
	downloader.Result
	edgeID string
}

// downloadWithManifestRetry는 URL이 만료됐거나 Edge에서 거부됐거나 모든
// Edge가 실패하면 매니페스트를 한 번 새로 받아 새 Edge 목록으로 시도한다.
// 호출자가 갱신된 CampaignID/TargetVersion을 사용할 수 있게 *manifest를
// 새 매니페스트로 덮어쓴다.
func (m *Machine) downloadWithManifestRetry(ctx context.Context, manifest *otaclient.Manifest) (edgeDownloadResult, error) {
	result, err := m.downloadFromEdges(ctx, manifest)
	if !errors.Is(err, errDownloadURLsExpired) &&
		!errors.Is(err, downloader.ErrManifestExpired) &&
		!errors.Is(err, errAllEdgesFailed) {
		return result, err
	}

	fresh, outcome, mErr := m.fetchManifest(ctx, manifest.CampaignID)
	switch outcome {
	case manifestNotTarget:
		return edgeDownloadResult{}, errAbortManifestNotTarget
	case manifestError:
		return edgeDownloadResult{}, fmt.Errorf("%w: %w", errAbortManifestError, mErr)
	}
	*manifest = fresh

	return m.downloadFromEdges(ctx, manifest)
}

func (m *Machine) downloadFromEdges(ctx context.Context, manifest *otaclient.Manifest) (edgeDownloadResult, error) {
	if expiresAt := manifest.URLExpiresAt; !expiresAt.IsZero() && !time.Now().Before(expiresAt) {
		return edgeDownloadResult{}, errDownloadURLsExpired
	}

	type edgeDownload struct {
		url    string
		edgeID string
	}
	edges := make([]edgeDownload, 1, 1+len(manifest.FallbackURLs))
	edges[0].url = manifest.DownloadURL
	if manifest.Edge != nil {
		edges[0].edgeID = manifest.Edge.EdgeID
	}
	for _, fallback := range manifest.FallbackURLs {
		edges = append(edges, edgeDownload{url: fallback.URL, edgeID: fallback.EdgeID})
	}

	var lastErr error
	for _, edge := range edges {
		result, err := m.downloadFromEdge(ctx, manifest, edge.url)
		// 해시가 달라도 이 Edge에서 파일을 받은 사실은 보고한다.
		if err == nil || downloadFailureReason(err) == state.FailureHashMismatch {
			return edgeDownloadResult{Result: result, edgeID: edge.edgeID}, err
		}
		if ctx.Err() != nil || !canSwitchEdge(err) {
			return edgeDownloadResult{}, err
		}
		lastErr = err
	}
	return edgeDownloadResult{}, fmt.Errorf("%w: %w", errAllEdgesFailed, lastErr)
}

func (m *Machine) downloadFromEdge(ctx context.Context, manifest *otaclient.Manifest, url string) (downloader.Result, error) {
	for retries := 0; ; retries++ {
		result, err := m.downloader.Download(ctx, manifest.CampaignID, url, manifest.FileSize, manifest.SHA256)
		if retries == edgeRetryLimit || !canRetryEdge(err) {
			return result, err
		}
		select {
		case <-ctx.Done():
			return downloader.Result{}, ctx.Err()
		case <-time.After(edgeRetryInterval):
			if err := ctx.Err(); err != nil {
				return downloader.Result{}, err
			}
		}
	}
}

func canRetryEdge(err error) bool {
	var httpErr *downloader.HTTPError
	return errors.As(err, &httpErr) &&
		(httpErr.StatusCode == http.StatusBadGateway || httpErr.StatusCode == http.StatusGatewayTimeout)
}

func canSwitchEdge(err error) bool {
	var httpErr *downloader.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= http.StatusInternalServerError && httpErr.StatusCode < 600
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var connErr *net.OpError
	if errors.As(err, &connErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// downloadFailureReason은 다운로더 에러를 체크인에 보고할 실패 사유로 바꾼다.
func downloadFailureReason(err error) state.FailureReason {
	var dlErr *downloader.Error
	if errors.As(err, &dlErr) && dlErr.Reason == downloader.FailureHashMismatch {
		return state.FailureHashMismatch
	}
	return state.FailureDownloadFailed
}
