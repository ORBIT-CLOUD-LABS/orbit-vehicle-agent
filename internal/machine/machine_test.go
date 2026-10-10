package machine_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/machine"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

var testIdentity = machine.VehicleIdentity{
	VehicleID: "v1",
	Model:     "model-x",
	HWVersion: "hw1",
	Region:    "kr",
}

// runMachine runs m.Run in a goroutine and waits for it to return, failing
// the test if it doesn't within a generous bound. Tests drive Run to
// completion either by cancelling ctx (from an onCheckIn/onGetManifest
// callback, once the scenario under test has happened) or with a short
// context timeout.
func runMachine(t *testing.T, m *machine.Machine, ctx context.Context, initialVersion string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx, initialVersion) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("machine.Run did not return in time")
		return nil
	}
}

// VEHICLE-REQ-002: 기동 시 등록해 토큰을 메모리에 보관한다.
// VEHICLE-REQ-001: 체크인 루프는 서버가 돌려준 nextCheckInSeconds를 반영한다.
func TestMachine_Run_RegistersThenLoopsCheckInsWithToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses: []otaclient.CheckInResponse{
			{UpdateTarget: false, NextCheckInSeconds: 0},
			{UpdateTarget: false, NextCheckInSeconds: 0},
		},
	}
	ota.onCheckIn = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, &fakeDownloader{}, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	require.Len(t, ota.registerCalls, 1)
	assert.Equal(t, "v1", ota.registerCalls[0].VehicleID)
	assert.Equal(t, "1.0.0", ota.registerCalls[0].CurrentVersion)

	require.GreaterOrEqual(t, len(ota.checkInCalls), 2)
	for _, token := range ota.checkInTokens {
		assert.Equal(t, "tok-1", token)
	}
}

// UPDATE-REQ-001: 체크인 200 OK 응답 후 보고했던 last_update를 지운다.
func TestMachine_Run_ClearsLastUpdateAfterSuccessfulCheckIn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: false, NextCheckInSeconds: 0}},
	}
	ota.onCheckIn = func(int) { cancel() }

	store := &fakeStore{load: state.Snapshot{
		VehicleID:      "v1",
		Status:         state.StatusIdle,
		CurrentVersion: "1.0.0",
		LastUpdate: &state.LastUpdate{
			CampaignID: "cmp-0",
			Result:     state.ResultSucceeded,
			FinishedAt: time.Now().Add(-time.Minute),
		},
	}}
	m := machine.New(ota, &fakeDownloader{}, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	saved, ok := store.lastSave()
	require.True(t, ok, "last_update를 지운 상태가 저장되어야 한다")
	assert.Nil(t, saved.LastUpdate)
	assert.Equal(t, state.StatusIdle, saved.Status)
	assert.Equal(t, "1.0.0", saved.CurrentVersion)
}

// 전체 성공 경로: MANIFEST-REQ-001, DOWNLOAD-REQ-001 →
// 매니페스트 → DOWNLOADING 저장 → 다운로드·검증 → INSTALLING 저장 → 설치 → 결과 저장 → IDLE.
func TestMachine_Run_SuccessfulUpdateFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestResponses: []otaclient.Manifest{{
			CampaignID:    "cmp-1",
			TargetVersion: "1.1.0",
			FileSize:      10,
			SHA256:        "abc",
			DownloadURL:   "https://cdn.example/cmp-1",
		}},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{results: []downloader.Result{{Path: "/data/downloads/cmp-1.bin", Size: 10}}}
	inst := &fakeInstaller{results: []installer.Result{{Path: "/data/firmware/current.bin"}}}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	require.Len(t, store.saves, 3, "DOWNLOADING, INSTALLING, 결과(IDLE) 순으로 저장되어야 한다")
	assert.Equal(t, state.StatusDownloading, store.saves[0].Status)
	require.NotNil(t, store.saves[0].PendingUpdate)
	assert.Equal(t, "cmp-1", store.saves[0].PendingUpdate.CampaignID)
	assert.Equal(t, state.StatusInstalling, store.saves[1].Status)

	final := store.saves[2]
	assert.Equal(t, state.StatusIdle, final.Status)
	assert.Nil(t, final.PendingUpdate)
	assert.Equal(t, "1.1.0", final.CurrentVersion)
	require.NotNil(t, final.LastUpdate)
	assert.Equal(t, state.ResultSucceeded, final.LastUpdate.Result)
	assert.Equal(t, "cmp-1", final.LastUpdate.CampaignID)

	assert.Equal(t, []string{"cmp-1"}, dl.calls)
	assert.Equal(t, []string{"/data/downloads/cmp-1.bin"}, inst.calls)
}

// DOWNLOAD-REQ-001: 다운로드 실패는 DOWNLOAD_FAILED로 저장하고 IDLE로 돌아간다.
func TestMachine_Run_DownloadFailure_RecordsDownloadFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestResponses: []otaclient.Manifest{{CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/cmp-1"}},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{errs: []error{&downloader.Error{Reason: downloader.FailureDownloadFailed, Err: errBoom}}}
	inst := &fakeInstaller{}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	final, ok := store.lastSave()
	require.True(t, ok)
	assert.Equal(t, state.StatusIdle, final.Status)
	assert.Equal(t, "1.0.0", final.CurrentVersion, "실패했으므로 버전은 바뀌지 않아야 한다")
	require.NotNil(t, final.LastUpdate)
	assert.Equal(t, state.ResultFailed, final.LastUpdate.Result)
	assert.Equal(t, state.FailureDownloadFailed, final.LastUpdate.FailureReason)
	assert.Empty(t, inst.calls, "다운로드에 실패하면 설치를 시도하지 않아야 한다")
}

// DOWNLOAD-REQ-001: SHA-256 불일치는 HASH_MISMATCH로 저장한다.
func TestMachine_Run_HashMismatch_RecordsHashMismatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestResponses: []otaclient.Manifest{{CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/cmp-1"}},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{errs: []error{&downloader.Error{Reason: downloader.FailureHashMismatch, Err: errBoom}}}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, dl, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	final, ok := store.lastSave()
	require.True(t, ok)
	require.NotNil(t, final.LastUpdate)
	assert.Equal(t, state.FailureHashMismatch, final.LastUpdate.FailureReason)
}

// 설치 §⑦ (docs/flow.md, 전용 REQ-ID 없음): 설치 실패는 INSTALL_FAILED로 저장한다.
func TestMachine_Run_InstallFailure_RecordsInstallFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestResponses: []otaclient.Manifest{{CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/cmp-1"}},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{results: []downloader.Result{{Path: "/data/downloads/cmp-1.bin", Size: 10}}}
	inst := &fakeInstaller{errs: []error{&installer.Error{Reason: installer.FailureInstallFailed, Err: errBoom}}}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	final, ok := store.lastSave()
	require.True(t, ok)
	assert.Equal(t, "1.0.0", final.CurrentVersion)
	require.NotNil(t, final.LastUpdate)
	assert.Equal(t, state.FailureInstallFailed, final.LastUpdate.FailureReason)
}

// MANIFEST-REQ-001: 매니페스트 404/409/410은 실패 결과 없이 IDLE로 돌아간다.
func TestMachine_Run_ManifestNotTarget_GoesIdleWithoutFailureResult(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"campaign not found", otaclient.CodeCampaignNotFound},
		{"not update target", otaclient.CodeNotUpdateTarget},
		{"campaign inactive", otaclient.CodeCampaignInactive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ota := &fakeOTA{
				registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
				checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
				manifestErrs:      []error{&otaclient.APIError{StatusCode: 409, Code: tt.code}},
			}
			ota.onCheckIn = func(int) { cancel() }

			dl := &fakeDownloader{}
			inst := &fakeInstaller{}
			store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
			m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

			err := runMachine(t, m, ctx, "1.0.0")
			require.ErrorIs(t, err, context.Canceled)

			require.Len(t, store.saves, 1)
			assert.Equal(t, state.StatusIdle, store.saves[0].Status)
			assert.Nil(t, store.saves[0].PendingUpdate)
			assert.Nil(t, store.saves[0].LastUpdate, "대상이 아닌 경우 실패 결과를 남기지 않아야 한다")
			assert.Empty(t, dl.calls)
			assert.Empty(t, inst.calls)
		})
	}
}

// 재시도 정책(이슈 #12): 매니페스트 조회 자체가 (404/409/410이 아닌 사유로) 실패하면
// 상태를 바꾸지 않고 다음 체크인 주기에 다시 시도한다.
func TestMachine_Run_ManifestTransientError_LeavesStateUnchangedForNextCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestErrs:      []error{errBoom},
	}
	ota.onCheckIn = func(int) { cancel() }

	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, &fakeDownloader{}, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	assert.Empty(t, store.saves, "일시적인 매니페스트 조회 실패는 상태를 저장하지 않아야 한다")
}

// DOWNLOAD-REQ-001: CDN이 서명 URL을 거부(403/410)하면 매니페스트를 다시 요청해 재다운로드한다.
func TestMachine_Run_CDNRejectsURL_RefetchesManifestAndRetriesDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestResponses: []otaclient.Manifest{
			{CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/old"},
			{CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/new"},
		},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{
		errs:    []error{downloader.ErrManifestExpired, nil},
		results: []downloader.Result{{}, {Path: "/data/downloads/cmp-1.bin", Size: 10}},
	}
	inst := &fakeInstaller{results: []installer.Result{{Path: "/data/firmware/current.bin"}}}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	require.Len(t, ota.manifestCalls, 2)
	require.Len(t, dl.calls, 2)

	final, ok := store.lastSave()
	require.True(t, ok)
	assert.Equal(t, state.StatusIdle, final.Status)
	require.NotNil(t, final.LastUpdate)
	assert.Equal(t, state.ResultSucceeded, final.LastUpdate.Result)
	assert.Equal(t, "1.1.0", final.CurrentVersion)
}

// 재시작 복구: DOWNLOADING 상태였으면 매니페스트를 다시 요청해 처음부터 다운로드한다.
func TestMachine_Run_RestartRecovery_Downloading_RefetchesManifestAndRestartsDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: false, NextCheckInSeconds: 0}},
		manifestResponses: []otaclient.Manifest{{CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/cmp-1"}},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{results: []downloader.Result{{Path: "/data/downloads/cmp-1.bin", Size: 10}}}
	inst := &fakeInstaller{results: []installer.Result{{Path: "/data/firmware/current.bin"}}}
	store := &fakeStore{load: state.Snapshot{
		VehicleID:      "v1",
		Status:         state.StatusDownloading,
		CurrentVersion: "1.0.0",
		PendingUpdate:  &state.PendingUpdate{CampaignID: "cmp-1", TargetVersion: "1.1.0"},
	}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	assert.Equal(t, "1.0.0", ota.registerCalls[0].CurrentVersion, "등록은 복구 전 버전으로 수행되어야 한다")
	require.Len(t, ota.manifestCalls, 1)
	require.Len(t, dl.calls, 1)
	require.Len(t, inst.calls, 1)

	require.GreaterOrEqual(t, len(ota.checkInCalls), 1)
	lastUpdate := ota.checkInCalls[0].LastUpdate
	require.NotNil(t, lastUpdate, "복구된 결과는 다음 체크인에 보고되어야 한다")
	assert.Equal(t, "SUCCEEDED", lastUpdate.Result)
	assert.Equal(t, "cmp-1", lastUpdate.CampaignID)
}

// 재시작 복구: INSTALLING 상태였으면 다시 다운로드하지 않고 설치만 재수행한다.
func TestMachine_Run_RestartRecovery_Installing_ResumesInstallWithoutRedownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: false, NextCheckInSeconds: 0}},
	}
	ota.onCheckIn = func(int) { cancel() }

	dl := &fakeDownloader{path: "/data/downloads/cmp-1.bin"}
	inst := &fakeInstaller{results: []installer.Result{{Path: "/data/firmware/current.bin"}}}
	store := &fakeStore{load: state.Snapshot{
		VehicleID:      "v1",
		Status:         state.StatusInstalling,
		CurrentVersion: "1.0.0",
		PendingUpdate:  &state.PendingUpdate{CampaignID: "cmp-1", TargetVersion: "1.1.0"},
	}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	assert.Empty(t, dl.calls, "INSTALLING 복구는 다시 다운로드하지 않아야 한다")
	assert.Empty(t, ota.manifestCalls, "INSTALLING 복구는 매니페스트를 다시 요청하지 않아야 한다")
	require.Equal(t, []string{"/data/downloads/cmp-1.bin"}, inst.calls)

	require.GreaterOrEqual(t, len(ota.checkInCalls), 1)
	lastUpdate := ota.checkInCalls[0].LastUpdate
	require.NotNil(t, lastUpdate)
	assert.Equal(t, "SUCCEEDED", lastUpdate.Result)
}

// VEHICLE-REQ-001: 체크인이 401이면 재등록 후 재개한다.
func TestMachine_Run_CheckInUnauthorized_ReregistersAndRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}, {VehicleToken: "tok-2"}},
		checkInErrs:       []error{&otaclient.APIError{StatusCode: 401, Code: otaclient.CodeUnauthorized}, nil},
		checkInResponses:  []otaclient.CheckInResponse{{}, {UpdateTarget: false, NextCheckInSeconds: 0}},
	}
	ota.onCheckIn = func(call int) {
		if call == 1 {
			cancel()
		}
	}

	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, &fakeDownloader{}, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	require.Len(t, ota.registerCalls, 2, "최초 등록 + 401 이후 재등록")
	require.Len(t, ota.checkInTokens, 2)
	assert.Equal(t, "tok-1", ota.checkInTokens[0])
	assert.Equal(t, "tok-2", ota.checkInTokens[1])
}

// MANIFEST-REQ-001: 매니페스트 조회가 401이면 재등록 후 재개한다.
func TestMachine_Run_GetManifestUnauthorized_ReregistersAndRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}, {VehicleToken: "tok-2"}},
		checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: "cmp-1", NextCheckInSeconds: 0}},
		manifestErrs:      []error{&otaclient.APIError{StatusCode: 401, Code: otaclient.CodeUnauthorized}, nil},
		manifestResponses: []otaclient.Manifest{{}, {CampaignID: "cmp-1", TargetVersion: "1.1.0", FileSize: 10, SHA256: "abc", DownloadURL: "https://cdn.example/cmp-1"}},
	}
	ota.onGetManifest = func(call int) {
		if call == 1 {
			cancel()
		}
	}

	dl := &fakeDownloader{results: []downloader.Result{{Path: "/data/downloads/cmp-1.bin", Size: 10}}}
	inst := &fakeInstaller{results: []installer.Result{{Path: "/data/firmware/current.bin"}}}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, dl, inst, store, zap.NewNop(), testIdentity, "enroll-key")

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.Canceled)

	require.Len(t, ota.registerCalls, 2)
	require.Len(t, ota.manifestTokens, 2)
	assert.Equal(t, "tok-1", ota.manifestTokens[0])
	assert.Equal(t, "tok-2", ota.manifestTokens[1])

	final, ok := store.lastSave()
	require.True(t, ok)
	require.NotNil(t, final.LastUpdate)
	assert.Equal(t, state.ResultSucceeded, final.LastUpdate.Result)
}

// flow.md ③: 등록(최초) 실패는 재시도 없이 시작 실패로 끝난다.
func TestMachine_Run_InitialRegistrationFailure_ReturnsErrorWithoutRetry(t *testing.T) {
	ota := &fakeOTA{
		registerErrs: []error{&otaclient.APIError{StatusCode: 401, Code: otaclient.CodeInvalidEnrollmentKey}},
	}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, &fakeDownloader{}, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "wrong-key")

	err := runMachine(t, m, context.Background(), "1.0.0")
	require.Error(t, err)
	assert.Empty(t, ota.checkInCalls, "등록에 실패하면 체크인 루프를 시작하지 않아야 한다")
}

// 재시도 정책(이슈 #12): 체크인 자체가 실패하면 실패 사유를 저장하지 않고
// 다음 체크인 주기(기본 간격)까지 기다렸다가 재시도한다.
func TestMachine_Run_CheckInFailure_WaitsDefaultIntervalBeforeRetry(t *testing.T) {
	ota := &fakeOTA{
		registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
		checkInErrs:       []error{errBoom},
	}
	store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
	m := machine.New(ota, &fakeDownloader{}, &fakeInstaller{}, store, zap.NewNop(), testIdentity, "enroll-key")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := runMachine(t, m, ctx, "1.0.0")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	assert.Len(t, ota.checkInCalls, 1, "기본 재시도 간격이 짧은 테스트 타임아웃보다 길어 다시 호출되지 않아야 한다")
	assert.Empty(t, store.saves, "체크인 실패는 실패 사유를 저장하지 않는다")
}
