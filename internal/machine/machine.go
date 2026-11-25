// Package machine은 차량 에이전트의 체크인·업데이트 상태 머신을 구현한다
// (orbit 레포 docs/flow.md §3, §4). 한 번 등록한 뒤 주기적으로 체크인하고,
// 캠페인 대상이 되면 매니페스트를 받아 펌웨어를 다운로드·검증·설치한 뒤
// 다음 체크인에서 결과를 보고한다.
package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

// defaultCheckInInterval은 WithCheckInInterval을 주지 않았을 때, 서버가 쓸 수
// 있는 nextCheckInSeconds를 주지 않거나 체크인 호출이 실패하면 쓰는 간격이다
// (VEHICLE-REQ-001. flow.md의 예시 응답이 30을 쓴다).
const defaultCheckInInterval = 30 * time.Second

const (
	edgeRetryLimit    = 2
	edgeRetryInterval = 250 * time.Millisecond
)

// OTAClient는 상태 머신이 호출하는 otaclient.Client의 메서드 집합이다.
type OTAClient interface {
	Register(ctx context.Context, enrollmentKey string, req otaclient.RegisterRequest) (otaclient.RegisterResponse, error)
	CheckIn(ctx context.Context, vehicleID, vehicleToken string, req otaclient.CheckInRequest) (otaclient.CheckInResponse, error)
	GetManifest(ctx context.Context, vehicleID string, campaignID int64, vehicleToken string) (otaclient.Manifest, error)
}

// Downloader는 상태 머신이 호출하는 downloader.Downloader의 메서드 집합이다.
type Downloader interface {
	Download(ctx context.Context, campaignID int64, downloadURL string, fileSize int64, expectedSHA256 string) (downloader.Result, error)
	Path(campaignID int64) string
}

// Installer는 상태 머신이 호출하는 installer.Installer의 메서드 집합이다.
type Installer interface {
	Install(ctx context.Context, sourcePath string) (installer.Result, error)
}

// StateStore는 상태 머신이 호출하는 state.Store의 메서드 집합이다.
type StateStore interface {
	Load(ctx context.Context, initialVersion string) (state.Snapshot, error)
	Save(ctx context.Context, snap state.Snapshot) error
}

// VehicleIdentity는 등록할 때마다 보내는 차량의 고정 식별 정보다
// (VEHICLE-REQ-002).
type VehicleIdentity struct {
	VehicleID string
	Model     string
	HWVersion string
	Region    string
}

// Machine은 차량 에이전트의 등록·체크인·업데이트 루프를 실행한다. 공개된
// 상태가 없으며 호출자는 Run으로만 구동한다.
type Machine struct {
	ota        OTAClient
	downloader Downloader
	installer  Installer
	store      StateStore
	verifier   SignatureVerifier
	logger     *zap.Logger

	identity      VehicleIdentity
	enrollmentKey string

	checkInInterval time.Duration

	vehicleToken         string
	snap                 state.Snapshot
	loggedSignatureSkips map[int64]struct{}
}

// Option은 Machine 설정을 바꾼다.
type Option func(*Machine)

// WithCheckInInterval은 서버가 쓸 수 있는 nextCheckInSeconds를 주지 않거나
// 체크인 호출이 실패했을 때 쓸 간격을 정한다 (CHECKIN_INTERVAL).
func WithCheckInInterval(d time.Duration) Option {
	return func(m *Machine) {
		m.checkInInterval = d
	}
}

// New는 Machine을 만든다. identity와 enrollmentKey는 (재)등록에 쓰고, 등록으로
// 받은 차량 토큰은 메모리에만 두며 저장하지 않는다. logger가 nil이면 아무것도
// 하지 않는 로거를 쓴다.
func New(ota OTAClient, dl Downloader, inst Installer, store StateStore, verifier SignatureVerifier, logger *zap.Logger, identity VehicleIdentity, enrollmentKey string, opts ...Option) *Machine {
	if logger == nil {
		logger = zap.NewNop()
	}
	m := &Machine{
		ota:             ota,
		downloader:      dl,
		installer:       inst,
		store:           store,
		verifier:        verifier,
		logger:          logger,
		identity:        identity,
		enrollmentKey:   enrollmentKey,
		checkInInterval: defaultCheckInInterval,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Run은 저장된 상태를 읽고 차량을 등록한 뒤, 재시작으로 중단된 업데이트를
// 이어서 하고, ctx가 취소될 때까지 체크인을 반복한다. initialVersion은 아직
// 저장된 상태가 없는 첫 실행의 버전이다.
//
// Run은 시작 자체를 못 할 때만 에러를 반환한다(상태 로드나 최초 등록 실패.
// flow.md ③: 등록 키가 틀리면 차량은 시작 실패로 끝난다). 루프가 돌기
// 시작한 뒤에는 한 주기 안의 실패를 반환하지 않고 로그로 남긴 뒤 다음
// 체크인에서 재시도한다. 이슈 #12에서 정한 재시도 정책("이번 시도 실패 →
// 다음 주기에 재시도")을 따른다.
func (m *Machine) Run(ctx context.Context, initialVersion string) error {
	snap, err := m.store.Load(ctx, initialVersion)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	m.snap = snap
	m.loggedSignatureSkips = make(map[int64]struct{})

	if err := m.register(ctx); err != nil {
		return fmt.Errorf("register vehicle: %w", err)
	}

	m.resume(ctx)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		nextCheckIn := m.tick(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nextCheckIn):
		}
	}
}

// resume은 디스크에서 읽은 상태를 보고, 재시작으로 중단된 업데이트를 이어서
// 한다.
func (m *Machine) resume(ctx context.Context) {
	switch m.snap.Status {
	case state.StatusDownloading:
		m.performUpdate(ctx, m.snap.PendingUpdate.CampaignID)
	case state.StatusInstalling:
		m.resumeInstall(ctx)
	}
}

// tick은 체크인 한 주기를 실행한다. 현재 버전과 보고할 결과를 보내고, 업데이트
// 대상이면 다음 체크인 전에 업데이트를 수행한다. 다음 tick까지 기다릴 시간을
// 반환한다.
func (m *Machine) tick(ctx context.Context) time.Duration {
	resp, err := m.checkIn(ctx)
	if err != nil {
		m.logger.Warn("check-in failed, retrying next cycle", zap.Error(err))
		return m.checkInInterval
	}

	if m.snap.LastUpdate != nil {
		cleared := m.snap
		cleared.LastUpdate = nil
		m.save(ctx, cleared)
	}

	if resp.UpdateTarget {
		m.performUpdate(ctx, resp.CampaignID)
	}

	if resp.NextCheckInSeconds <= 0 {
		return m.checkInInterval
	}
	return time.Duration(resp.NextCheckInSeconds) * time.Second
}

// performUpdate는 campaignID의 매니페스트를 받아 펌웨어를 다운로드·검증하고
// 설치한다. DOWNLOADING/INSTALLING 전이는 해당 동작 전에 먼저 저장한다
// (이슈 #12: "상태 전이는 다음 상태를 먼저 저장한 뒤 동작합니다"). 도중의
// 실패는 결과로 기록하고 IDLE로 돌아간다. 일시적 실패는 상태를 바꾸지 않아
// 다음 체크인 주기에 처음부터 다시 시도한다.
func (m *Machine) performUpdate(ctx context.Context, campaignID int64) {
	if rejection, rejected := m.snap.SignatureRejections[campaignID]; rejected &&
		(rejection.MissingKeyID == "" || !m.verifier.HasKey(rejection.MissingKeyID)) {
		if _, logged := m.loggedSignatureSkips[campaignID]; !logged {
			m.logger.Info("skipping campaign after firmware signature rejection",
				zap.Int64("campaign_id", campaignID),
				zap.String("failure_reason", string(state.FailureSignatureInvalid)),
				zap.String("missing_key_id", rejection.MissingKeyID),
			)
			m.loggedSignatureSkips[campaignID] = struct{}{}
		}
		return
	}
	manifest, outcome, err := m.fetchManifest(ctx, campaignID)
	switch outcome {
	case manifestNotTarget:
		m.backToIdle(ctx)
		return
	case manifestError:
		m.logger.Warn("fetch manifest failed, retrying next cycle", zap.Error(err))
		return
	}

	downloading := m.snap
	downloading.Status = state.StatusDownloading
	downloading.PendingUpdate = &state.PendingUpdate{CampaignID: manifest.CampaignID, TargetVersion: manifest.TargetVersion}
	if !m.save(ctx, downloading) {
		return
	}

	result, dlErr := m.downloadWithManifestRetry(ctx, &manifest)
	if dlErr != nil {
		if errors.Is(dlErr, errAbortManifestNotTarget) {
			m.backToIdle(ctx)
			return
		}
		if errors.Is(dlErr, errAbortManifestError) {
			m.logger.Warn("refresh manifest failed, retrying next cycle", zap.Error(dlErr))
			return
		}
		m.recordFailure(ctx, manifest.CampaignID, result.edgeID, downloadFailureReason(dlErr), dlErr.Error())
		return
	}

	pending := &state.PendingUpdate{
		CampaignID:    manifest.CampaignID,
		TargetVersion: manifest.TargetVersion,
		EdgeID:        result.edgeID,
		Verification: &state.FirmwareVerification{
			Size:      result.Size,
			SHA256:    result.SHA256,
			Signature: manifest.Signature,
			KeyID:     manifest.KeyID,
		},
	}
	if !m.verifySignature(ctx, pending, result.Size, result.SHA256) {
		return
	}
	if !m.canInstallVersion(ctx, pending) {
		return
	}
	installing := m.snap
	installing.Status = state.StatusInstalling
	installing.PendingUpdate = pending
	if _, rejected := installing.SignatureRejections[campaignID]; rejected {
		installing.SignatureRejections = maps.Clone(installing.SignatureRejections)
		delete(installing.SignatureRejections, campaignID)
	}
	if !m.save(ctx, installing) {
		return
	}

	m.finishInstall(ctx, pending, result.Path)
}

func (m *Machine) finishInstall(ctx context.Context, pending *state.PendingUpdate, sourcePath string) {
	_, err := m.installer.Install(ctx, sourcePath)
	if err != nil {
		reason, detail := classifyInstallFailure(err)
		m.recordFailure(ctx, pending.CampaignID, pending.EdgeID, reason, detail)
		return
	}
	m.recordSuccess(ctx, pending)
}

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

// manifestOutcome은 매니페스트 조회 결과를 분류해, 호출자가 "더 이상 대상
// 아님"(flow.md ⑤: 404/409/410)과 재시도할 만한 일시적 실패를 구분하게 한다.
type manifestOutcome int

const (
	manifestOK manifestOutcome = iota
	manifestNotTarget
	manifestError
)

func (m *Machine) fetchManifest(ctx context.Context, campaignID int64) (otaclient.Manifest, manifestOutcome, error) {
	manifest, err := m.getManifest(ctx, campaignID)
	if err == nil {
		return manifest, manifestOK, nil
	}

	var apiErr *otaclient.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case otaclient.CodeCampaignNotFound, otaclient.CodeNotUpdateTarget, otaclient.CodeCampaignInactive:
			return otaclient.Manifest{}, manifestNotTarget, nil
		}
	}
	return otaclient.Manifest{}, manifestError, err
}

// recordFailure는 실패한 업데이트 결과를 저장하고 IDLE로 돌아가, 다음
// 체크인에서 실패를 보고할 수 있게 한다.
func (m *Machine) recordFailure(ctx context.Context, campaignID int64, edgeID string, reason state.FailureReason, detail string) {
	m.save(ctx, m.failedUpdate(campaignID, edgeID, reason, detail))
}

func (m *Machine) failedUpdate(campaignID int64, edgeID string, reason state.FailureReason, detail string) state.Snapshot {
	next := m.snap
	next.Status = state.StatusIdle
	next.PendingUpdate = nil
	next.LastUpdate = &state.LastUpdate{
		CampaignID:    campaignID,
		Result:        state.ResultFailed,
		FailureReason: reason,
		FailureDetail: detail,
		EdgeID:        edgeID,
		FinishedAt:    time.Now(),
	}
	return next
}

// recordSuccess는 성공한 업데이트 결과를 저장하고 새 버전의 IDLE로 돌아가,
// 다음 체크인에서 성공을 보고할 수 있게 한다.
func (m *Machine) recordSuccess(ctx context.Context, pending *state.PendingUpdate) {
	next := m.snap
	next.Status = state.StatusIdle
	next.CurrentVersion = pending.TargetVersion
	next.PendingUpdate = nil
	next.LastUpdate = &state.LastUpdate{
		CampaignID: pending.CampaignID,
		Result:     state.ResultSucceeded,
		EdgeID:     pending.EdgeID,
		FinishedAt: time.Now(),
	}
	m.save(ctx, next)
}

// backToIdle은 결과를 기록하지 않고 진행 중인 업데이트를 지운다. 차량이 더
// 이상 대상이 아닌 경우에 쓴다 (flow.md ⑤: 404/409/410은 실패 결과 없이 바로
// IDLE).
func (m *Machine) backToIdle(ctx context.Context) {
	next := m.snap
	next.Status = state.StatusIdle
	next.PendingUpdate = nil
	m.save(ctx, next)
}

// save는 snap을 저장하고, 성공했을 때만 머신의 현재 스냅샷으로 삼는다.
// 실패하면 로그를 남기고 현재 스냅샷을 그대로 둔다. 그래서 호출자가 진행하던
// 전이는 버려지고, 다음 주기에 마지막으로 저장된 상태부터 다시 시도한다.
func (m *Machine) save(ctx context.Context, snap state.Snapshot) bool {
	if err := m.store.Save(ctx, snap); err != nil {
		m.logger.Warn("persist state failed, retrying next cycle", zap.Error(err))
		return false
	}
	m.snap = snap
	return true
}

// register는 차량을 등록하고 받은 토큰을 메모리에 둔다.
func (m *Machine) register(ctx context.Context) error {
	resp, err := m.ota.Register(ctx, m.enrollmentKey, otaclient.RegisterRequest{
		VehicleID:      m.identity.VehicleID,
		Model:          m.identity.Model,
		HWVersion:      m.identity.HWVersion,
		Region:         m.identity.Region,
		CurrentVersion: m.snap.CurrentVersion,
	})
	if err != nil {
		return err
	}
	m.vehicleToken = resp.VehicleToken
	return nil
}

// callWithReregister는 call을 호출하고, CodeUnauthorized로 실패하면 다시
// 등록해 새 토큰을 받은 뒤 한 번 더 호출한다 (flow.md ③: "재시작하거나
// 체크인에서 401을 받으면 다시 등록한다").
func (m *Machine) callWithReregister(ctx context.Context, call func() error) error {
	err := call()

	var apiErr *otaclient.APIError
	if errors.As(err, &apiErr) && apiErr.Code == otaclient.CodeUnauthorized {
		if regErr := m.register(ctx); regErr != nil {
			return fmt.Errorf("re-register after unauthorized: %w", regErr)
		}
		err = call()
	}
	return err
}

func (m *Machine) checkIn(ctx context.Context) (otaclient.CheckInResponse, error) {
	var resp otaclient.CheckInResponse
	err := m.callWithReregister(ctx, func() error {
		var innerErr error
		resp, innerErr = m.ota.CheckIn(ctx, m.identity.VehicleID, m.vehicleToken, otaclient.CheckInRequest{
			CurrentVersion: m.snap.CurrentVersion,
			LastUpdate:     toAPILastUpdate(m.snap.LastUpdate),
		})
		return innerErr
	})
	return resp, err
}

func (m *Machine) getManifest(ctx context.Context, campaignID int64) (otaclient.Manifest, error) {
	var resp otaclient.Manifest
	err := m.callWithReregister(ctx, func() error {
		var innerErr error
		resp, innerErr = m.ota.GetManifest(ctx, m.identity.VehicleID, campaignID, m.vehicleToken)
		return innerErr
	})
	return resp, err
}

func toAPILastUpdate(last *state.LastUpdate) *otaclient.LastUpdate {
	if last == nil {
		return nil
	}
	return &otaclient.LastUpdate{
		CampaignID:    last.CampaignID,
		Result:        string(last.Result),
		FailureReason: string(last.FailureReason),
		FailureDetail: last.FailureDetail,
		EdgeID:        last.EdgeID,
		FinishedAt:    last.FinishedAt,
	}
}

// downloadFailureReason은 다운로더 에러를 체크인에 보고할 실패 사유로 바꾼다.
func downloadFailureReason(err error) state.FailureReason {
	var dlErr *downloader.Error
	if errors.As(err, &dlErr) && dlErr.Reason == downloader.FailureHashMismatch {
		return state.FailureHashMismatch
	}
	return state.FailureDownloadFailed
}

// classifyInstallFailure는 설치기 실패를 에이전트의 실패 사유(flow.md ④의
// 표)와 사람이 읽을 상세 내용으로 바꾼다.
func classifyInstallFailure(err error) (state.FailureReason, string) {
	return state.FailureInstallFailed, err.Error()
}
