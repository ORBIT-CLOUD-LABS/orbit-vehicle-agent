package machine_test

import (
	"context"
	"errors"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

// fakeOTA는 machine.OTAClient의 손으로 만든 테스트 대역이다. 메서드마다 미리
// 정한 결과를 순서대로 쓰고, 다 쓰면 마지막 결과를 반복한다.
// onCheckIn/onGetManifest로 호출에 반응할 수 있다(예: 검증할 상황이 일어난 뒤
// 구동 중인 context를 취소).
type fakeOTA struct {
	registerResponses []otaclient.RegisterResponse
	registerErrs      []error
	registerCalls     []otaclient.RegisterRequest

	checkInResponses []otaclient.CheckInResponse
	checkInErrs      []error
	checkInCalls     []otaclient.CheckInRequest
	checkInTokens    []string
	onCheckIn        func(call int)

	manifestResponses []otaclient.Manifest
	manifestErrs      []error
	manifestCalls     []string
	manifestTokens    []string
	onGetManifest     func(call int)
}

func (f *fakeOTA) Register(_ context.Context, _ string, req otaclient.RegisterRequest) (otaclient.RegisterResponse, error) {
	i := len(f.registerCalls)
	f.registerCalls = append(f.registerCalls, req)
	return pick(f.registerResponses, i), pick(f.registerErrs, i)
}

func (f *fakeOTA) CheckIn(_ context.Context, _, vehicleToken string, req otaclient.CheckInRequest) (otaclient.CheckInResponse, error) {
	i := len(f.checkInCalls)
	f.checkInCalls = append(f.checkInCalls, req)
	f.checkInTokens = append(f.checkInTokens, vehicleToken)
	if f.onCheckIn != nil {
		f.onCheckIn(i)
	}
	return pick(f.checkInResponses, i), pick(f.checkInErrs, i)
}

func (f *fakeOTA) GetManifest(_ context.Context, _, campaignID, vehicleToken string) (otaclient.Manifest, error) {
	i := len(f.manifestCalls)
	f.manifestCalls = append(f.manifestCalls, campaignID)
	f.manifestTokens = append(f.manifestTokens, vehicleToken)
	if f.onGetManifest != nil {
		f.onGetManifest(i)
	}
	return pick(f.manifestResponses, i), pick(f.manifestErrs, i)
}

// pick은 items[i]를 반환한다. i가 범위를 넘으면 마지막 항목을, items가 비어
// 있으면 zero value를 반환한다.
func pick[T any](items []T, i int) T {
	if len(items) == 0 {
		var zero T
		return zero
	}
	if i >= len(items) {
		i = len(items) - 1
	}
	return items[i]
}

// fakeDownloader는 machine.Downloader의 손으로 만든 테스트 대역이다.
type fakeDownloader struct {
	results []downloader.Result
	errs    []error
	calls   []string // 호출된 campaignID 목록
	path    string
}

func (f *fakeDownloader) Download(_ context.Context, campaignID, _ string, _ int64, _ string) (downloader.Result, error) {
	i := len(f.calls)
	f.calls = append(f.calls, campaignID)
	return pick(f.results, i), pick(f.errs, i)
}

func (f *fakeDownloader) Path(campaignID string) string {
	if f.path != "" {
		return f.path
	}
	return "/data/downloads/" + campaignID + ".bin"
}

// fakeInstaller는 machine.Installer의 손으로 만든 테스트 대역이다.
type fakeInstaller struct {
	results []installer.Result
	errs    []error
	calls   []string // 호출된 sourcePath 목록
}

func (f *fakeInstaller) Install(_ context.Context, sourcePath string) (installer.Result, error) {
	i := len(f.calls)
	f.calls = append(f.calls, sourcePath)
	return pick(f.results, i), pick(f.errs, i)
}

// fakeStore는 machine.StateStore의 손으로 만든 테스트 대역이다. Load는
// initialVersion을 무시하고 load를 그대로 반환한다(테스트가 머신의 시작
// 스냅샷을 직접 정한다).
type fakeStore struct {
	load    state.Snapshot
	loadErr error
	saveErr error
	saves   []state.Snapshot
}

func (f *fakeStore) Load(_ context.Context, _ string) (state.Snapshot, error) {
	return f.load, f.loadErr
}

func (f *fakeStore) Save(_ context.Context, snap state.Snapshot) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves = append(f.saves, snap)
	return nil
}

// lastSave는 마지막으로 저장된 스냅샷을 반환한다. 저장된 적이 없으면 false를
// 반환한다.
func (f *fakeStore) lastSave() (state.Snapshot, bool) {
	if len(f.saves) == 0 {
		return state.Snapshot{}, false
	}
	return f.saves[len(f.saves)-1], true
}

var errBoom = errors.New("boom")
