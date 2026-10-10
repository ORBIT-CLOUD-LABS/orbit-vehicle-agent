// Package state persists the vehicle agent's lifecycle state to disk and
// recovers it after a restart.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Status is the vehicle agent's current lifecycle phase.
type Status string

const (
	StatusIdle        Status = "IDLE"
	StatusDownloading Status = "DOWNLOADING"
	StatusInstalling  Status = "INSTALLING"
)

// Result is the outcome of an update attempt, reported on the next check-in.
type Result string

const (
	ResultSucceeded Result = "SUCCEEDED"
	ResultFailed    Result = "FAILED"
)

// FailureReason classifies why an update attempt failed.
type FailureReason string

const (
	FailureDownloadFailed FailureReason = "DOWNLOAD_FAILED"
	FailureHashMismatch   FailureReason = "HASH_MISMATCH"
	FailureInstallFailed  FailureReason = "INSTALL_FAILED"
)

// PendingUpdate identifies the campaign being downloaded or installed.
type PendingUpdate struct {
	CampaignID    string `json:"campaign_id"`
	TargetVersion string `json:"target_version"`
}

// LastUpdate is the most recent update result, reported on the next check-in
// and cleared once the server acknowledges it.
type LastUpdate struct {
	CampaignID    string        `json:"campaign_id"`
	Result        Result        `json:"result"`
	FailureReason FailureReason `json:"failure_reason,omitempty"`
	FailureDetail string        `json:"failure_detail,omitempty"`
	FinishedAt    time.Time     `json:"finished_at"`
}

// Snapshot is the vehicle agent's persisted lifecycle state.
type Snapshot struct {
	VehicleID      string         `json:"vehicle_id"`
	Status         Status         `json:"status"`
	CurrentVersion string         `json:"current_version"`
	PendingUpdate  *PendingUpdate `json:"pending_update,omitempty"`
	LastUpdate     *LastUpdate    `json:"last_update,omitempty"`
}

// Store reads and writes a single vehicle's Snapshot to DATA_DIR/state.json.
type Store struct {
	path      string
	vehicleID string
}

// NewStore returns a Store bound to vehicleID's state.json under dataDir.
func NewStore(dataDir, vehicleID string) *Store {
	return &Store{
		path:      filepath.Join(dataDir, "state.json"),
		vehicleID: vehicleID,
	}
}

// Load reads the persisted Snapshot. If no state file exists yet, it
// initializes one at StatusIdle with initialVersion and persists it.
// Load refuses to return a Snapshot whose vehicle_id does not match the
// Store's configured vehicle ID.
func (s *Store) Load(ctx context.Context, initialVersion string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		initial := Snapshot{
			VehicleID:      s.vehicleID,
			Status:         StatusIdle,
			CurrentVersion: initialVersion,
		}
		if err := s.Save(ctx, initial); err != nil {
			return Snapshot{}, fmt.Errorf("save initial state: %w", err)
		}
		return initial, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read state file: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("parse state file: %w", err)
	}

	if snap.VehicleID != s.vehicleID {
		return Snapshot{}, fmt.Errorf("state file vehicle_id %q does not match configured vehicle_id %q", snap.VehicleID, s.vehicleID)
	}
	if err := validate(snap); err != nil {
		return Snapshot{}, fmt.Errorf("invalid state file: %w", err)
	}

	return snap, nil
}

// Save validates snap and atomically persists it: it writes a temp file in
// DATA_DIR and renames it over state.json.
func (s *Store) Save(ctx context.Context, snap Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if snap.VehicleID != s.vehicleID {
		return fmt.Errorf("state vehicle_id %q does not match configured vehicle_id %q", snap.VehicleID, s.vehicleID)
	}
	if err := validate(snap); err != nil {
		return fmt.Errorf("invalid state: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename temp state file: %w", err)
	}
	return nil
}

func validate(snap Snapshot) error {
	if snap.VehicleID == "" {
		return fmt.Errorf("vehicle_id is required")
	}
	if snap.CurrentVersion == "" {
		return fmt.Errorf("current_version is required")
	}

	switch snap.Status {
	case StatusIdle:
		if snap.PendingUpdate != nil {
			return fmt.Errorf("status %q: pending_update must be empty", snap.Status)
		}
	case StatusDownloading, StatusInstalling:
		if err := validatePendingUpdate(snap.Status, snap.PendingUpdate); err != nil {
			return err
		}
	default:
		return fmt.Errorf("status %q: must be one of %s, %s, %s", snap.Status, StatusIdle, StatusDownloading, StatusInstalling)
	}

	if snap.LastUpdate != nil {
		if err := validateLastUpdate(snap.LastUpdate); err != nil {
			return err
		}
	}

	return nil
}

func validatePendingUpdate(status Status, pending *PendingUpdate) error {
	if pending == nil {
		return fmt.Errorf("status %q: pending_update is required", status)
	}
	if pending.CampaignID == "" {
		return fmt.Errorf("pending_update.campaign_id is required")
	}
	if pending.TargetVersion == "" {
		return fmt.Errorf("pending_update.target_version is required")
	}
	return nil
}

func validateLastUpdate(last *LastUpdate) error {
	if last.CampaignID == "" {
		return fmt.Errorf("last_update.campaign_id is required")
	}
	if last.FinishedAt.IsZero() {
		return fmt.Errorf("last_update.finished_at is required")
	}

	switch last.Result {
	case ResultSucceeded:
		if last.FailureReason != "" {
			return fmt.Errorf("last_update.failure_reason must be empty when result is %s", ResultSucceeded)
		}
	case ResultFailed:
		switch last.FailureReason {
		case FailureDownloadFailed, FailureHashMismatch, FailureInstallFailed:
		default:
			return fmt.Errorf("last_update.failure_reason %q: must be one of %s, %s, %s", last.FailureReason, FailureDownloadFailed, FailureHashMismatch, FailureInstallFailed)
		}
	default:
		return fmt.Errorf("last_update.result %q: must be %s or %s", last.Result, ResultSucceeded, ResultFailed)
	}

	return nil
}
