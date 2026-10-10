package machine_test

import (
	"context"
	"errors"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

// fakeOTA is a hand-written test double for machine.OTAClient. Each method's
// scripted results are consumed in order; the last one repeats once
// exhausted. onCheckIn/onGetManifest let a test react to a call (e.g. to
// cancel the driving context once the scenario under test has happened).
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

// pick returns items[i], or the last item if i is beyond the slice, or the
// zero value if items is empty.
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

// fakeDownloader is a hand-written test double for machine.Downloader.
type fakeDownloader struct {
	results []downloader.Result
	errs    []error
	calls   []string // campaignIDs
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

// fakeInstaller is a hand-written test double for machine.Installer.
type fakeInstaller struct {
	results []installer.Result
	errs    []error
	calls   []string // sourcePaths
}

func (f *fakeInstaller) Install(_ context.Context, sourcePath string) (installer.Result, error) {
	i := len(f.calls)
	f.calls = append(f.calls, sourcePath)
	return pick(f.results, i), pick(f.errs, i)
}

// fakeStore is a hand-written test double for machine.StateStore. load is
// returned as-is from Load, ignoring initialVersion (tests set up the
// snapshot they want the machine to start from directly).
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

// lastSave returns the most recently saved snapshot, or false if nothing
// was ever saved.
func (f *fakeStore) lastSave() (state.Snapshot, bool) {
	if len(f.saves) == 0 {
		return state.Snapshot{}, false
	}
	return f.saves[len(f.saves)-1], true
}

var errBoom = errors.New("boom")
