// Package machine implements the vehicle agent's check-in and update state
// machine (docs/flow.md §3, §4 in the orbit repo): register once, loop
// periodic check-ins, and when targeted by a campaign, fetch its manifest,
// download and verify the firmware, install it, and report the result on
// the next check-in.
package machine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

// defaultCheckInInterval is used when a check-in call itself fails, so the
// agent has no server-provided nextCheckInSeconds to fall back on
// (VEHICLE-REQ-001; flow.md's example response uses 30).
const defaultCheckInInterval = 30 * time.Second

// OTAClient is the subset of otaclient.Client the state machine calls.
type OTAClient interface {
	Register(ctx context.Context, enrollmentKey string, req otaclient.RegisterRequest) (otaclient.RegisterResponse, error)
	CheckIn(ctx context.Context, vehicleID, vehicleToken string, req otaclient.CheckInRequest) (otaclient.CheckInResponse, error)
	GetManifest(ctx context.Context, vehicleID, campaignID, vehicleToken string) (otaclient.Manifest, error)
}

// Downloader is the subset of downloader.Downloader the state machine calls.
type Downloader interface {
	Download(ctx context.Context, campaignID, downloadURL string, fileSize int64, expectedSHA256 string) (downloader.Result, error)
	Path(campaignID string) string
}

// Installer is the subset of installer.Installer the state machine calls.
type Installer interface {
	Install(ctx context.Context, sourcePath string) (installer.Result, error)
}

// StateStore is the subset of state.Store the state machine calls.
type StateStore interface {
	Load(ctx context.Context, initialVersion string) (state.Snapshot, error)
	Save(ctx context.Context, snap state.Snapshot) error
}

// VehicleIdentity is the vehicle's static identity, sent on every
// registration (VEHICLE-REQ-002).
type VehicleIdentity struct {
	VehicleID string
	Model     string
	HWVersion string
	Region    string
}

// Machine runs the vehicle agent's register/check-in/update loop. It keeps
// no exported state; callers drive it only through Run.
type Machine struct {
	ota        OTAClient
	downloader Downloader
	installer  Installer
	store      StateStore
	logger     *zap.Logger

	identity      VehicleIdentity
	enrollmentKey string

	vehicleToken string
	snap         state.Snapshot
}

// New creates a Machine. identity and enrollmentKey are used for
// (re-)registration; the vehicle token obtained from it is kept in memory
// only, never persisted. If logger is nil, a no-op logger is used.
func New(ota OTAClient, dl Downloader, inst Installer, store StateStore, logger *zap.Logger, identity VehicleIdentity, enrollmentKey string) *Machine {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Machine{
		ota:           ota,
		downloader:    dl,
		installer:     inst,
		store:         store,
		logger:        logger,
		identity:      identity,
		enrollmentKey: enrollmentKey,
	}
}

// Run loads persisted state, registers the vehicle, resumes any update that
// was interrupted by a restart, and then loops check-ins until ctx is
// cancelled. initialVersion seeds the very first run, before any state has
// been persisted.
//
// Run returns an error only when it cannot start at all (loading state or
// the initial registration fails, per flow.md ③: an invalid enrollment key
// ends the vehicle's startup). Once the loop is running, failures within a
// single cycle are logged and retried on the next check-in rather than
// returned, per the agreed retry policy (issue #12): this attempt fails,
// retry next cycle.
func (m *Machine) Run(ctx context.Context, initialVersion string) error {
	snap, err := m.store.Load(ctx, initialVersion)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	m.snap = snap

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

// resume continues an update that was interrupted by a restart, based on
// the status loaded from disk.
func (m *Machine) resume(ctx context.Context) {
	switch m.snap.Status {
	case state.StatusDownloading:
		m.performUpdate(ctx, m.snap.PendingUpdate.CampaignID)
	case state.StatusInstalling:
		m.resumeInstall(ctx)
	}
}

// tick runs one check-in cycle: report the current version and any pending
// result, and if the vehicle is now an update target, perform the update
// before the next check-in. It returns how long to wait before the next
// call to tick.
func (m *Machine) tick(ctx context.Context) time.Duration {
	resp, err := m.checkIn(ctx)
	if err != nil {
		m.logger.Warn("check-in failed, retrying next cycle", zap.Error(err))
		return defaultCheckInInterval
	}

	if m.snap.LastUpdate != nil {
		cleared := m.snap
		cleared.LastUpdate = nil
		m.save(ctx, cleared)
	}

	if resp.UpdateTarget {
		m.performUpdate(ctx, resp.CampaignID)
	}

	return time.Duration(resp.NextCheckInSeconds) * time.Second
}

// performUpdate fetches campaignID's manifest, downloads and verifies the
// firmware, and installs it, persisting the DOWNLOADING/INSTALLING
// transitions before each corresponding action (issue #12: "상태 전이는
// 다음 상태를 먼저 저장한 뒤 동작합니다"). Any failure along the way is
// recorded and the machine returns to IDLE; transient failures leave the
// snapshot unchanged so the next check-in cycle retries from scratch.
func (m *Machine) performUpdate(ctx context.Context, campaignID string) {
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
			m.logger.Warn("refresh manifest after cdn url rejection failed, retrying next cycle", zap.Error(dlErr))
			return
		}
		reason, detail := classifyDownloadFailure(dlErr)
		m.recordFailure(ctx, manifest.CampaignID, reason, detail)
		return
	}

	installing := m.snap
	installing.Status = state.StatusInstalling
	if !m.save(ctx, installing) {
		return
	}

	m.finishInstall(ctx, manifest.CampaignID, manifest.TargetVersion, result.Path)
}

// resumeInstall re-runs Install for an update whose download already
// finished before a restart interrupted installation (issue #12: "INSTALLING
// 은 설치 재수행").
func (m *Machine) resumeInstall(ctx context.Context) {
	pending := m.snap.PendingUpdate
	sourcePath := m.downloader.Path(pending.CampaignID)
	m.finishInstall(ctx, pending.CampaignID, pending.TargetVersion, sourcePath)
}

func (m *Machine) finishInstall(ctx context.Context, campaignID, targetVersion, sourcePath string) {
	_, err := m.installer.Install(ctx, sourcePath)
	if err != nil {
		reason, detail := classifyInstallFailure(err)
		m.recordFailure(ctx, campaignID, reason, detail)
		return
	}
	m.recordSuccess(ctx, campaignID, targetVersion)
}

// errAbortManifestNotTarget and errAbortManifestError let
// downloadWithManifestRetry report a manifest-refresh outcome through the
// single error return downloader.Download's signature allows.
var (
	errAbortManifestNotTarget = errors.New("manifest refresh: no longer an update target")
	errAbortManifestError     = errors.New("manifest refresh failed")
)

// downloadWithManifestRetry downloads manifest's firmware. If the CDN
// rejects the signed URL (downloader.ErrManifestExpired), it fetches a
// fresh manifest once and retries the download with it (issue #12: "CDN
// URL 거부 시 매니페스트 재요청 후 재다운로드"). *manifest is updated in
// place to the fresh manifest when that happens, since the caller needs its
// (possibly new) CampaignID/TargetVersion afterwards.
func (m *Machine) downloadWithManifestRetry(ctx context.Context, manifest *otaclient.Manifest) (downloader.Result, error) {
	result, err := m.downloader.Download(ctx, manifest.CampaignID, manifest.DownloadURL, manifest.FileSize, manifest.SHA256)
	if err == nil || !errors.Is(err, downloader.ErrManifestExpired) {
		return result, err
	}

	fresh, outcome, mErr := m.fetchManifest(ctx, manifest.CampaignID)
	switch outcome {
	case manifestNotTarget:
		return downloader.Result{}, errAbortManifestNotTarget
	case manifestError:
		return downloader.Result{}, fmt.Errorf("%w: %w", errAbortManifestError, mErr)
	}
	*manifest = fresh

	return m.downloader.Download(ctx, manifest.CampaignID, manifest.DownloadURL, manifest.FileSize, manifest.SHA256)
}

// manifestOutcome classifies a manifest fetch so callers can tell "no
// longer a target" (flow.md ⑤: 404/409/410) apart from a transient failure
// worth retrying.
type manifestOutcome int

const (
	manifestOK manifestOutcome = iota
	manifestNotTarget
	manifestError
)

func (m *Machine) fetchManifest(ctx context.Context, campaignID string) (otaclient.Manifest, manifestOutcome, error) {
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

// recordFailure persists a failed update result and returns the machine to
// IDLE, ready to report the failure on the next check-in.
func (m *Machine) recordFailure(ctx context.Context, campaignID string, reason state.FailureReason, detail string) {
	next := state.Snapshot{
		VehicleID:      m.snap.VehicleID,
		Status:         state.StatusIdle,
		CurrentVersion: m.snap.CurrentVersion,
		LastUpdate: &state.LastUpdate{
			CampaignID:    campaignID,
			Result:        state.ResultFailed,
			FailureReason: reason,
			FailureDetail: detail,
			FinishedAt:    time.Now(),
		},
	}
	m.save(ctx, next)
}

// recordSuccess persists a successful update result and returns the
// machine to IDLE at the new version, ready to report success on the next
// check-in.
func (m *Machine) recordSuccess(ctx context.Context, campaignID, targetVersion string) {
	next := state.Snapshot{
		VehicleID:      m.snap.VehicleID,
		Status:         state.StatusIdle,
		CurrentVersion: targetVersion,
		LastUpdate: &state.LastUpdate{
			CampaignID: campaignID,
			Result:     state.ResultSucceeded,
			FinishedAt: time.Now(),
		},
	}
	m.save(ctx, next)
}

// backToIdle clears any pending update without recording a result, for the
// case where the vehicle turns out not to be a target anymore (flow.md ⑤:
// 404/409/410 — no failure result, straight back to IDLE).
func (m *Machine) backToIdle(ctx context.Context) {
	next := m.snap
	next.Status = state.StatusIdle
	next.PendingUpdate = nil
	m.save(ctx, next)
}

// save persists snap and, only on success, makes it the machine's current
// snapshot. On failure it logs and leaves the current snapshot unchanged,
// so the caller's in-progress transition is abandoned and retried from the
// last successfully persisted state on the next cycle.
func (m *Machine) save(ctx context.Context, snap state.Snapshot) bool {
	if err := m.store.Save(ctx, snap); err != nil {
		m.logger.Warn("persist state failed, retrying next cycle", zap.Error(err))
		return false
	}
	m.snap = snap
	return true
}

// register enrolls the vehicle and stores the returned token in memory.
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

// callWithReregister calls call, and if it fails with CodeUnauthorized,
// re-registers to obtain a fresh token and calls it once more (flow.md ③:
// "재시작하거나 체크인에서 401을 받으면 다시 등록한다").
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

func (m *Machine) getManifest(ctx context.Context, campaignID string) (otaclient.Manifest, error) {
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
		FinishedAt:    last.FinishedAt,
	}
}

// classifyDownloadFailure maps a downloader failure to this agent's
// failure vocabulary (flow.md ④'s table) and a human-readable detail.
func classifyDownloadFailure(err error) (state.FailureReason, string) {
	var dlErr *downloader.Error
	if errors.As(err, &dlErr) && dlErr.Reason == downloader.FailureHashMismatch {
		return state.FailureHashMismatch, dlErr.Error()
	}
	return state.FailureDownloadFailed, err.Error()
}

// classifyInstallFailure maps an installer failure to this agent's failure
// vocabulary (flow.md ④'s table) and a human-readable detail.
func classifyInstallFailure(err error) (state.FailureReason, string) {
	return state.FailureInstallFailed, err.Error()
}
