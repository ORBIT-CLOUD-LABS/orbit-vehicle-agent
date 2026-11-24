package machine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/firmwaresignature"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

// SignatureVerifier는 설치할 펌웨어의 서명을 검증한다.
type SignatureVerifier interface {
	HasKey(keyID string) bool
	Verify(keyID, signature string, metadata firmwaresignature.Metadata) error
}

func (m *Machine) verifySignature(ctx context.Context, pending *state.PendingUpdate, size int64, hash string) bool {
	verification := pending.Verification
	err := m.verifier.Verify(verification.KeyID, verification.Signature, firmwaresignature.Metadata{
		Model: m.identity.Model, Version: pending.TargetVersion, Size: size, SHA256: hash,
	})
	if err != nil {
		next := m.failedUpdate(pending.CampaignID, pending.EdgeID, state.FailureSignatureInvalid, err.Error())
		rejection := state.SignatureRejection{}
		if errors.Is(err, firmwaresignature.ErrUnknownKey) {
			rejection.MissingKeyID = verification.KeyID
		}
		next.SignatureRejections = maps.Clone(next.SignatureRejections)
		if next.SignatureRejections == nil {
			next.SignatureRejections = make(map[int64]state.SignatureRejection)
		}
		next.SignatureRejections[pending.CampaignID] = rejection
		m.save(ctx, next)
		return false
	}
	return true
}

// resumeInstall은 저장된 파일의 크기·해시·서명을 다시 확인한 뒤 설치를 재개한다.
func (m *Machine) resumeInstall(ctx context.Context) {
	pending := m.snap.PendingUpdate
	verification := pending.Verification
	if verification == nil || verification.SHA256 == "" {
		m.recordFailure(ctx, pending.CampaignID, pending.EdgeID, state.FailureSignatureInvalid, "missing firmware verification data")
		return
	}

	path := m.downloader.Path(pending.CampaignID)
	size, hash, err := measureFirmware(ctx, path)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		m.recordFailure(ctx, pending.CampaignID, pending.EdgeID, state.FailureDownloadFailed, err.Error())
		return
	}
	if size != verification.Size {
		detail := fmt.Sprintf("downloaded size %d does not match expected %d", size, verification.Size)
		m.recordFailure(ctx, pending.CampaignID, pending.EdgeID, state.FailureDownloadFailed, detail)
		return
	}
	if hash != verification.SHA256 {
		detail := fmt.Sprintf("sha256 %s does not match expected %s", hash, verification.SHA256)
		m.recordFailure(ctx, pending.CampaignID, pending.EdgeID, state.FailureHashMismatch, detail)
		return
	}
	if m.verifySignature(ctx, pending, size, hash) {
		m.finishInstall(ctx, pending, path)
	}
}

func measureFirmware(ctx context.Context, path string) (int64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("open downloaded firmware: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return 0, "", fmt.Errorf("hash downloaded firmware: %w", err)
	}
	return size, fmt.Sprintf("%x", hasher.Sum(nil)), nil
}
