package machine_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/firmwaresignature"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/machine"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

func TestMachine_Run_InstallingRevalidatesFirmware(t *testing.T) {
	const firmware = "firmware"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(firmware)))
	invalidSignature := errors.New("firmware signature verification failed")
	tests := []struct {
		name        string
		content     string
		legacyState bool
		verifyErr   error
		wantFailure state.FailureReason
		wantDetail  string
	}{
		{name: "valid firmware", content: firmware},
		{name: "changed size", content: "short", wantFailure: state.FailureDownloadFailed, wantDetail: "downloaded size"},
		{name: "changed firmware of same size", content: "tampered", wantFailure: state.FailureHashMismatch, wantDetail: "sha256"},
		{name: "invalid signature", content: firmware, verifyErr: invalidSignature, wantFailure: state.FailureSignatureInvalid, wantDetail: invalidSignature.Error()},
		{name: "legacy state without verification data", content: firmware, legacyState: true, wantFailure: state.FailureSignatureInvalid, wantDetail: "missing firmware verification data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			path := filepath.Join(dir, "1.bin")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			pending := &state.PendingUpdate{CampaignID: 1, TargetVersion: "1.1.0", EdgeID: "edge-daejeon"}
			if !tt.legacyState {
				pending.Verification = &state.FirmwareVerification{Size: int64(len(firmware)), SHA256: hash, Signature: "stored-signature", KeyID: "v1"}
			}
			store := state.NewStore(dir, testIdentity.VehicleID)
			require.NoError(t, store.Save(ctx, state.Snapshot{
				VehicleID: testIdentity.VehicleID, Status: state.StatusInstalling, CurrentVersion: "1.0.0", PendingUpdate: pending,
			}))

			ota := &fakeOTA{onCheckIn: func(int) { cancel() }}
			dl := &fakeDownloader{path: path}
			inst := &fakeInstaller{}
			verifier := &fakeSignatureVerifier{err: tt.verifyErr}
			m := machine.New(ota, dl, inst, store, verifier, nil, testIdentity, "enroll-key", fastCheckIn)
			require.ErrorIs(t, runMachine(t, m, ctx, "1.0.0"), context.Canceled)

			assert.Empty(t, dl.calls)
			assert.Empty(t, ota.manifestCalls)
			require.Len(t, ota.checkInCalls, 1)
			update := ota.checkInCalls[0].LastUpdate
			require.NotNil(t, update)
			assert.Equal(t, "edge-daejeon", update.EdgeID)
			if tt.wantFailure == "" || tt.verifyErr != nil {
				assert.Equal(t, []signatureCall{{
					keyID: "v1", signature: "stored-signature",
					metadata: firmwaresignature.Metadata{Model: testIdentity.Model, Version: "1.1.0", SHA256: hash, Size: int64(len(firmware))},
				}}, verifier.calls)
			} else {
				assert.Empty(t, verifier.calls)
			}
			if tt.wantFailure == "" {
				assert.Equal(t, []string{path}, inst.calls)
				assert.Equal(t, "SUCCEEDED", update.Result)
				return
			}
			assert.Empty(t, inst.calls)
			assert.Equal(t, "1.0.0", ota.checkInCalls[0].CurrentVersion)
			assert.Equal(t, "FAILED", update.Result)
			assert.Equal(t, string(tt.wantFailure), update.FailureReason)
			assert.Contains(t, update.FailureDetail, tt.wantDetail)
		})
	}
}

func TestMachine_Run_ChecksVersionBeforeInstall(t *testing.T) {
	const firmware = "firmware"
	const campaignID int64 = 1
	const edgeID = "edge-daejeon"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(firmware)))
	badSignature := errors.New("firmware signature verification failed")
	tests := []struct {
		name         string
		current      string
		target       string
		verifyErr    error
		wantInstall  bool
		wantFailure  state.FailureReason
		wantDetail   string
		downloadOnly bool
	}{
		{name: "higher", current: "1.0.9", target: "1.0.10", wantInstall: true},
		{name: "equal", current: "1.0.0", target: "1.0"},
		{name: "lower", current: "1.0.10", target: "1.0.9", wantFailure: state.FailureDowngradeRejected, wantDetail: "lower than current version"},
		{name: "invalid target", current: "1.0", target: "v2.0", wantFailure: state.FailureDowngradeRejected, wantDetail: "parse target version"},
		{name: "invalid current", current: "v1.0", target: "2.0", wantFailure: state.FailureDowngradeRejected, wantDetail: "parse current version"},
		{name: "empty target", current: "1.0", wantFailure: state.FailureDowngradeRejected, wantDetail: "parse target version", downloadOnly: true},
		{name: "invalid signature on lower version", current: "2.0", target: "1.0", verifyErr: badSignature, wantFailure: state.FailureSignatureInvalid, wantDetail: badSignature.Error()},
	}
	paths := []struct {
		name   string
		resume bool
	}{
		{name: "download"},
		{name: "resume", resume: true},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			for _, tt := range tests {
				if path.resume && tt.downloadOnly {
					continue
				}
				t.Run(tt.name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					dir := t.TempDir()
					firmwarePath := filepath.Join(dir, "1.bin")
					snap := state.Snapshot{
						VehicleID: testIdentity.VehicleID, Status: state.StatusIdle, CurrentVersion: tt.current,
					}
					ota := &fakeOTA{}
					dl := &fakeDownloader{}
					reportIndex := 0
					if path.resume {
						require.NoError(t, os.WriteFile(firmwarePath, []byte(firmware), 0o600))
						snap.Status = state.StatusInstalling
						snap.PendingUpdate = &state.PendingUpdate{
							CampaignID: campaignID, TargetVersion: tt.target, EdgeID: edgeID,
							Verification: &state.FirmwareVerification{Size: int64(len(firmware)), SHA256: hash, Signature: "signature", KeyID: "v1"},
						}
						dl.path = firmwarePath
					} else {
						reportIndex = 1
						ota.checkInResponses = []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: campaignID}, {}}
						ota.manifestResponses = []otaclient.Manifest{{
							CampaignID: campaignID, TargetVersion: tt.target, FileSize: int64(len(firmware)), SHA256: hash,
							DownloadURL: "https://cdn.example/cmp-1", Edge: &otaclient.ManifestEdge{EdgeID: edgeID}, Signature: "signature", KeyID: "v1",
						}}
						dl.results = []downloader.Result{{Path: firmwarePath, Size: int64(len(firmware)), SHA256: hash}}
					}
					ota.onCheckIn = func(call int) {
						if call == reportIndex {
							cancel()
						}
					}
					store := state.NewStore(dir, testIdentity.VehicleID)
					require.NoError(t, store.Save(ctx, snap))
					inst := &fakeInstaller{}
					verifier := &fakeSignatureVerifier{err: tt.verifyErr}
					m := machine.New(ota, dl, inst, store, verifier, nil, testIdentity, "enroll-key", fastCheckIn)
					require.ErrorIs(t, runMachine(t, m, ctx, tt.current), context.Canceled)

					require.Len(t, ota.checkInCalls, reportIndex+1)
					report := ota.checkInCalls[reportIndex]
					require.Len(t, verifier.calls, 1)
					assert.Equal(t, tt.target, verifier.calls[0].metadata.Version)
					final, err := store.Load(context.Background(), tt.current)
					require.NoError(t, err)
					assert.Equal(t, state.StatusIdle, final.Status)
					assert.Nil(t, final.PendingUpdate)
					if tt.wantInstall {
						assert.Equal(t, []string{firmwarePath}, inst.calls)
						assert.Equal(t, tt.target, report.CurrentVersion)
						require.NotNil(t, report.LastUpdate)
						assert.Equal(t, "SUCCEEDED", report.LastUpdate.Result)
						return
					}
					assert.Empty(t, inst.calls)
					assert.Equal(t, tt.current, report.CurrentVersion)
					if tt.wantFailure == "" {
						assert.Nil(t, report.LastUpdate)
						return
					}
					require.NotNil(t, report.LastUpdate)
					assert.Equal(t, campaignID, report.LastUpdate.CampaignID)
					assert.Equal(t, "FAILED", report.LastUpdate.Result)
					assert.Equal(t, string(tt.wantFailure), report.LastUpdate.FailureReason)
					assert.Contains(t, report.LastUpdate.FailureDetail, tt.wantDetail)
					assert.Equal(t, edgeID, report.LastUpdate.EdgeID)
				})
			}
		})
	}
}

func TestMachine_Run_SignatureRejectionSurvivesCheckInAndRestart(t *testing.T) {
	const campaignID int64 = 1
	const keyID = "rotated-test-key"
	embedded, err := firmwaresignature.New()
	require.NoError(t, err)
	upgraded := &fakeSignatureVerifier{knownKeyID: keyID}
	badSignature := errors.New("firmware signature verification failed")

	type boot struct {
		verifier   machine.SignatureVerifier
		wantResult state.Result
		wantDetail string
	}
	tests := []struct {
		name        string
		legacyState bool
		boots       []boot
	}{
		{
			name: "unknown key is retried once the same key is available",
			boots: []boot{
				{verifier: embedded, wantResult: state.ResultFailed, wantDetail: "unknown keyId: " + keyID},
				{verifier: embedded},
				{verifier: &fakeSignatureVerifier{knownKeyID: "different-key"}},
				{verifier: upgraded, wantResult: state.ResultSucceeded},
			},
		},
		{
			name: "invalid signature remains rejected after upgrade",
			boots: []boot{
				{verifier: &fakeSignatureVerifier{knownKeyID: keyID, err: badSignature}, wantResult: state.ResultFailed, wantDetail: badSignature.Error()},
				{verifier: upgraded},
			},
		},
		{
			name:        "legacy rejection with no recorded reason is rechecked",
			legacyState: true,
			boots:       []boot{{verifier: upgraded, wantResult: state.ResultSucceeded}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.legacyState {
				legacy := `{"vehicle_id":"v1","status":"IDLE","current_version":"1.0.0","signature_rejected_campaigns":[1]}`
				require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600))
			}
			for index, boot := range tt.boots {
				t.Run(fmt.Sprintf("boot %d", index+1), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					ota := &fakeOTA{
						checkInResponses: []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: campaignID}},
						manifestResponses: []otaclient.Manifest{{
							CampaignID: campaignID, TargetVersion: "1.1.0", KeyID: keyID,
							Edge: &otaclient.ManifestEdge{EdgeID: "edge-seoul"},
						}},
						onCheckIn: func(call int) {
							if call == 2 {
								cancel()
							}
						},
					}
					if boot.wantResult == state.ResultSucceeded {
						ota.checkInResponses = append(ota.checkInResponses, otaclient.CheckInResponse{})
					}
					dl := &fakeDownloader{results: []downloader.Result{{Path: "firmware.bin"}}}
					inst := &fakeInstaller{}
					store := state.NewStore(dir, testIdentity.VehicleID)
					core, logs := observer.New(zap.InfoLevel)
					m := machine.New(ota, dl, inst, store, boot.verifier, zap.New(core), testIdentity, "enroll-key", fastCheckIn)
					require.ErrorIs(t, runMachine(t, m, ctx, "1.0.0"), context.Canceled)

					skips := logs.FilterMessage("skipping campaign after firmware signature rejection").All()
					if boot.wantResult == state.ResultSucceeded {
						assert.Empty(t, skips)
					} else {
						require.Len(t, skips, 1)
						assert.Equal(t, campaignID, skips[0].ContextMap()["campaign_id"])
						assert.Equal(t, string(state.FailureSignatureInvalid), skips[0].ContextMap()["failure_reason"])
					}
					require.Len(t, ota.checkInCalls, 3)
					assert.Nil(t, ota.checkInCalls[2].LastUpdate)
					if boot.wantResult == "" {
						assert.Empty(t, ota.manifestCalls)
						assert.Empty(t, dl.calls)
						assert.Empty(t, inst.calls)
						return
					}
					assert.Equal(t, []int64{campaignID}, ota.manifestCalls)
					assert.Equal(t, []int64{campaignID}, dl.calls)
					update := ota.checkInCalls[1].LastUpdate
					require.NotNil(t, update)
					assert.Equal(t, string(boot.wantResult), update.Result)
					assert.Equal(t, "edge-seoul", update.EdgeID)
					if boot.wantResult == state.ResultSucceeded {
						assert.Equal(t, []string{"firmware.bin"}, inst.calls)
						saved, err := store.Load(context.Background(), "1.0.0")
						require.NoError(t, err)
						assert.Empty(t, saved.SignatureRejections)
						return
					}
					assert.Empty(t, inst.calls)
					assert.Equal(t, string(state.FailureSignatureInvalid), update.FailureReason)
					assert.Equal(t, boot.wantDetail, update.FailureDetail)
				})
			}
		})
	}
}
