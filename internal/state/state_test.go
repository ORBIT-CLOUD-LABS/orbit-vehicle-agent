package state_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

// UPDATE-REQ-001
func TestStore_Load_InitializesOnFirstBoot(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir, "car-0001")

	snap, err := store.Load(context.Background(), "1.0.0")

	require.NoError(t, err)
	assert.Equal(t, "car-0001", snap.VehicleID)
	assert.Equal(t, state.StatusIdle, snap.Status)
	assert.Equal(t, "1.0.0", snap.CurrentVersion)
	assert.Nil(t, snap.PendingUpdate)
	assert.FileExists(t, filepath.Join(dir, "state.json"))
}

// UPDATE-REQ-001
func TestStore_SaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir, "car-0001")
	ctx := context.Background()

	want := state.Snapshot{
		VehicleID:      "car-0001",
		Status:         state.StatusDownloading,
		CurrentVersion: "1.0.0",
		PendingUpdate: &state.PendingUpdate{
			CampaignID:    "cmp-1",
			TargetVersion: "1.1.0",
		},
	}
	require.NoError(t, store.Save(ctx, want))

	got, err := store.Load(ctx, "1.0.0")

	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// UPDATE-REQ-001
func TestStore_SaveThenLoad_WithLastUpdate(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir, "car-0001")
	ctx := context.Background()

	want := state.Snapshot{
		VehicleID:      "car-0001",
		Status:         state.StatusIdle,
		CurrentVersion: "1.0.0",
		LastUpdate: &state.LastUpdate{
			CampaignID:    "cmp-1",
			Result:        state.ResultFailed,
			FailureReason: state.FailureHashMismatch,
			FailureDetail: "sha256 mismatch",
			FinishedAt:    time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		},
	}
	require.NoError(t, store.Save(ctx, want))

	got, err := store.Load(ctx, "1.0.0")

	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// UPDATE-REQ-001
func TestStore_Save_AtomicallyReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir, "car-0001")
	ctx := context.Background()

	first := state.Snapshot{VehicleID: "car-0001", Status: state.StatusIdle, CurrentVersion: "1.0.0"}
	require.NoError(t, store.Save(ctx, first))

	second := state.Snapshot{VehicleID: "car-0001", Status: state.StatusIdle, CurrentVersion: "1.1.0"}
	require.NoError(t, store.Save(ctx, second))

	got, err := store.Load(ctx, "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", got.CurrentVersion)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no leftover temp file should remain")
}

// UPDATE-REQ-001
func TestStore_Load_RejectsVehicleIDMismatch(t *testing.T) {
	dir := t.TempDir()
	writer := state.NewStore(dir, "car-0001")
	require.NoError(t, writer.Save(context.Background(), state.Snapshot{
		VehicleID:      "car-0001",
		Status:         state.StatusIdle,
		CurrentVersion: "1.0.0",
	}))

	reader := state.NewStore(dir, "car-0002")
	_, err := reader.Load(context.Background(), "1.0.0")

	require.Error(t, err)
}

// UPDATE-REQ-001
func TestStore_Save_RejectsInvalidState(t *testing.T) {
	tests := []struct {
		name string
		snap state.Snapshot
	}{
		{
			name: "알 수 없는 status",
			snap: state.Snapshot{VehicleID: "car-0001", Status: "UNKNOWN", CurrentVersion: "1.0.0"},
		},
		{
			name: "IDLE인데 pending_update가 있음",
			snap: state.Snapshot{
				VehicleID:      "car-0001",
				Status:         state.StatusIdle,
				CurrentVersion: "1.0.0",
				PendingUpdate:  &state.PendingUpdate{CampaignID: "cmp-1", TargetVersion: "1.1.0"},
			},
		},
		{
			name: "DOWNLOADING인데 pending_update가 없음",
			snap: state.Snapshot{VehicleID: "car-0001", Status: state.StatusDownloading, CurrentVersion: "1.0.0"},
		},
		{
			name: "INSTALLING인데 pending_update.campaign_id가 없음",
			snap: state.Snapshot{
				VehicleID:      "car-0001",
				Status:         state.StatusInstalling,
				CurrentVersion: "1.0.0",
				PendingUpdate:  &state.PendingUpdate{TargetVersion: "1.1.0"},
			},
		},
		{
			name: "last_update.result이 FAILED인데 failure_reason이 없음",
			snap: state.Snapshot{
				VehicleID:      "car-0001",
				Status:         state.StatusIdle,
				CurrentVersion: "1.0.0",
				LastUpdate: &state.LastUpdate{
					CampaignID: "cmp-1",
					Result:     state.ResultFailed,
					FinishedAt: time.Now(),
				},
			},
		},
		{
			name: "last_update.result이 SUCCEEDED인데 failure_reason이 있음",
			snap: state.Snapshot{
				VehicleID:      "car-0001",
				Status:         state.StatusIdle,
				CurrentVersion: "1.0.0",
				LastUpdate: &state.LastUpdate{
					CampaignID:    "cmp-1",
					Result:        state.ResultSucceeded,
					FailureReason: state.FailureInstallFailed,
					FinishedAt:    time.Now(),
				},
			},
		},
		{
			name: "last_update.finished_at이 없음",
			snap: state.Snapshot{
				VehicleID:      "car-0001",
				Status:         state.StatusIdle,
				CurrentVersion: "1.0.0",
				LastUpdate: &state.LastUpdate{
					CampaignID: "cmp-1",
					Result:     state.ResultSucceeded,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := state.NewStore(dir, "car-0001")

			err := store.Save(context.Background(), tt.snap)

			require.Error(t, err)
		})
	}
}

// UPDATE-REQ-001
func TestStore_Load_RejectsCorruptStateFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte("not json"), 0o600))
	store := state.NewStore(dir, "car-0001")

	_, err := store.Load(context.Background(), "1.0.0")

	require.Error(t, err)
}
