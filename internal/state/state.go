// 상태는 차량 에이전트의 수명 주기 상태를 디스크에 유지하고 재시작 후 복구합니다.

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

// Status는 차량 에이전트의 현재 수명 주기 단계를 나타냅니다.
type Status string

const (
	StatusIdle        Status = "IDLE"
	StatusDownloading Status = "DOWNLOADING"
	StatusInstalling  Status = "INSTALLING"
)

// 결과는 업데이트 시도의 결과이며, 다음 체크인에서 보고됩니다.
type Result string

const (
	ResultSucceeded Result = "SUCCEEDED"
	ResultFailed    Result = "FAILED"
)

// FailureReason는 업데이트 시도가 실패한 이유를 분류합니다.
type FailureReason string

const (
	FailureDownloadFailed FailureReason = "DOWNLOAD_FAILED"
	FailureHashMismatch   FailureReason = "HASH_MISMATCH"
	FailureInstallFailed  FailureReason = "INSTALL_FAILED"
)

// PendingUpdate는 다운로드 또는 설치 중인 캠페인을 식별합니다.
type PendingUpdate struct {
	CampaignID    string `json:"campaign_id"`
	TargetVersion string `json:"target_version"`
}

// LastUpdate는 가장 최근의 업데이트 결과이며, 다음 체크인에서 보고되고 서버가 확인하면 지워집니다.
type LastUpdate struct {
	CampaignID    string        `json:"campaign_id"`
	Result        Result        `json:"result"`
	FailureReason FailureReason `json:"failure_reason,omitempty"`
	FailureDetail string        `json:"failure_detail,omitempty"`
	FinishedAt    time.Time     `json:"finished_at"`
}

// Snapshot은 차량 에이전트의 지속적인 수명 주기 상태입니다.
type Snapshot struct {
	VehicleID      string         `json:"vehicle_id"`
	Status         Status         `json:"status"`
	CurrentVersion string         `json:"current_version"`
	PendingUpdate  *PendingUpdate `json:"pending_update,omitempty"`
	LastUpdate     *LastUpdate    `json:"last_update,omitempty"`
}

// Store는 단일 차량의 Snapshot을 DATA_DIR/state.json에서 읽고 씁니다.
type Store struct {
	path      string
	vehicleID string
}

// NewStore는 dataDir 아래 vehicleID의 state.json에 바인딩된 Store를 반환합니다.
func NewStore(dataDir, vehicleID string) *Store {
	return &Store{
		path:      filepath.Join(dataDir, "state.json"),
		vehicleID: vehicleID,
	}
}

// Load는 지속적인 Snapshot을 읽습니다. 아직 상태 파일이 없으면 StatusIdle에서 initialVersion으로 초기화하고 저장합니다.
// Load는 Store의 구성된 차량 ID와 일치하지 않는 Snapshot을 반환하지 않습니다.
// Save는 snap을 검증하고 원자적으로 저장합니다: DATA_DIR에 임시 파일을 쓰고 state.json 위로 이름을 바꿉니다.

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
			return Snapshot{}, fmt.Errorf("최초 상태 저장: %w", err)
		}
		return initial, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("상태 파일 읽기: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("상태 파일 파싱: %w", err)
	}

	if snap.VehicleID != s.vehicleID {
		return Snapshot{}, fmt.Errorf("vehicle_id 상태파일은 %q 구성된 vehicle_id와 일치하지 않습니다. %q", snap.VehicleID, s.vehicleID)
	}
	if err := validate(snap); err != nil {
		return Snapshot{}, fmt.Errorf("타당하지 않은 상태 파일: %w", err)
	}

	return snap, nil
}

// Save는 snap을 검증하고 원자적으로 저장합니다: DATA_DIR에 임시 파일을 쓰고 state.json 위로 이름을 바꿉니다.

func (s *Store) Save(ctx context.Context, snap Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if snap.VehicleID != s.vehicleID {
		return fmt.Errorf("vehicle_id 상태는 %q 구성된 vehicle_id와 일치하지 않습니다. %q", snap.VehicleID, s.vehicleID)
	}
	if err := validate(snap); err != nil {
		return fmt.Errorf("타당하지 않은 상태: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("상태를 JSON으로 변환: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("데이터 디렉토리 생성: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("임시 상태 파일 쓰기: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("임시 상태 파일 이름 변경: %w", err)
	}
	return nil
}

func validate(snap Snapshot) error {
	if snap.VehicleID == "" {
		return fmt.Errorf("vehicle_id 은 필수입니다.")
	}
	if snap.CurrentVersion == "" {
		return fmt.Errorf("current_version 은 필수 입니다.")
	}

	switch snap.Status {
	case StatusIdle:
		if snap.PendingUpdate != nil {
			return fmt.Errorf("상태 %q: pending_update는 비어 있어야 합니다.", snap.Status)
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
		return fmt.Errorf("상태 %q: pending_update는 필수입니다.", status)
	}
	if pending.CampaignID == "" {
		return fmt.Errorf("pending_update.campaign_id는 필수입니다.")
	}
	if pending.TargetVersion == "" {
		return fmt.Errorf("pending_update.target_version은 필수입니다.")
	}
	return nil
}

func validateLastUpdate(last *LastUpdate) error {
	if last.CampaignID == "" {
		return fmt.Errorf("last_update.campaign_id는 필수입니다.")
	}
	if last.FinishedAt.IsZero() {
		return fmt.Errorf("last_update.finished_at은 필수입니다.")
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
