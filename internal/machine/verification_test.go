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

func TestMachine_Run_SignatureRejectionSurvivesCheckInAndRestart(t *testing.T) {
	dir := t.TempDir()
	const campaignID int64 = 1
	const unknownKeyID = "unknown-test-key"
	for _, restart := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		ota := &fakeOTA{
			checkInResponses: []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: campaignID}},
			manifestResponses: []otaclient.Manifest{{
				CampaignID: campaignID, TargetVersion: "1.1.0", KeyID: unknownKeyID,
				Edge: &otaclient.ManifestEdge{EdgeID: "edge-seoul"},
			}},
			onCheckIn: func(call int) {
				if call == 2 {
					cancel()
				}
			},
		}
		dl := &fakeDownloader{results: []downloader.Result{{Path: "firmware.bin"}}}
		inst := &fakeInstaller{}
		verifier, err := firmwaresignature.New()
		require.NoError(t, err)
		store := state.NewStore(dir, testIdentity.VehicleID)
		m := machine.New(ota, dl, inst, store, verifier, nil, testIdentity, "enroll-key", fastCheckIn)
		runErr := runMachine(t, m, ctx, "1.0.0")
		cancel()
		require.ErrorIs(t, runErr, context.Canceled)

		assert.Empty(t, inst.calls)
		require.Len(t, ota.checkInCalls, 3)
		assert.Nil(t, ota.checkInCalls[2].LastUpdate)
		if restart {
			assert.Empty(t, ota.manifestCalls)
			assert.Empty(t, dl.calls)
			continue
		}
		assert.Equal(t, []int64{campaignID}, ota.manifestCalls)
		assert.Equal(t, []int64{campaignID}, dl.calls)
		update := ota.checkInCalls[1].LastUpdate
		require.NotNil(t, update)
		assert.Equal(t, "FAILED", update.Result)
		assert.Equal(t, string(state.FailureSignatureInvalid), update.FailureReason)
		assert.Equal(t, "unknown keyId: "+unknownKeyID, update.FailureDetail)
		assert.Equal(t, "edge-seoul", update.EdgeID)
	}
}
