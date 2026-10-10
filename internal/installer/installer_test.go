package installer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
)

func writeSource(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}

// 설치 §⑦ (docs/flow.md, 전용 REQ-ID 없음)
func TestInstaller_Install_Success(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()
	content := []byte("verified-firmware-bytes")
	sourcePath := writeSource(t, workDir, "verified.bin", content)

	i := installer.New(dataDir)

	result, err := i.Install(context.Background(), sourcePath)
	require.NoError(t, err)

	wantPath := filepath.Join(dataDir, "firmware", "current.bin")
	assert.Equal(t, wantPath, result.Path)

	installed, err := os.ReadFile(wantPath)
	require.NoError(t, err)
	assert.Equal(t, content, installed)

	_, err = os.Stat(wantPath + ".tmp")
	assert.True(t, os.IsNotExist(err), "temp file should not remain after a successful install")
}

// 설치 §⑦ (docs/flow.md, 전용 REQ-ID 없음) — 재시작 후 같은 입력으로 재실행해도 결과가 같아야 한다
func TestInstaller_Install_IdempotentAcrossRestarts(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()
	content := []byte("verified-firmware-bytes")
	sourcePath := writeSource(t, workDir, "verified.bin", content)

	first := installer.New(dataDir)
	firstResult, err := first.Install(context.Background(), sourcePath)
	require.NoError(t, err)

	// 재시작을 흉내 내기 위해 새 Installer 인스턴스를 사용한다 (메모리 상태 없음).
	second := installer.New(dataDir)
	secondResult, err := second.Install(context.Background(), sourcePath)
	require.NoError(t, err)

	assert.Equal(t, firstResult, secondResult)

	installed, err := os.ReadFile(secondResult.Path)
	require.NoError(t, err)
	assert.Equal(t, content, installed)

	entries, err := os.ReadDir(filepath.Join(dataDir, "firmware"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "재실행 후에도 설치 위치에는 current.bin 하나만 남아야 한다")
}

// 설치 §⑦ (docs/flow.md, 전용 REQ-ID 없음)
func TestInstaller_Install_SourceFileMissing(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()
	missingPath := filepath.Join(workDir, "does-not-exist.bin")

	i := installer.New(dataDir)

	_, err := i.Install(context.Background(), missingPath)
	require.Error(t, err)

	var installErr *installer.Error
	require.ErrorAs(t, err, &installErr)

	_, statErr := os.Stat(filepath.Join(dataDir, "firmware", "current.bin"))
	assert.True(t, os.IsNotExist(statErr), "실패한 설치는 current.bin을 남기지 않아야 한다")
}

// 설치 §⑦ (docs/flow.md, 전용 REQ-ID 없음) — 설치에 실패해도 이전에 설치된 버전은 그대로 남아야 한다
func TestInstaller_Install_FailureLeavesPreviousInstallIntact(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()
	firstContent := []byte("good-firmware-v1")
	goodSource := writeSource(t, workDir, "good.bin", firstContent)

	i := installer.New(dataDir)
	firstResult, err := i.Install(context.Background(), goodSource)
	require.NoError(t, err)

	missingPath := filepath.Join(workDir, "does-not-exist.bin")
	_, err = i.Install(context.Background(), missingPath)
	require.Error(t, err)

	installed, err := os.ReadFile(firstResult.Path)
	require.NoError(t, err)
	assert.Equal(t, firstContent, installed, "실패한 설치 시도가 기존 설치를 덮어쓰면 안 된다")
}
