package durablefile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/durablefile"
)

// 상태·다운로드·설치 파일 쓰기에 fsync 적용
func TestWriteFile_CreatesFinalFileAndRemovesTemp(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "state.json")
	tmp := final + ".tmp"

	require.NoError(t, durablefile.WriteFile(tmp, final, []byte("hello"), 0o600))

	got, err := os.ReadFile(final)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	_, err = os.Stat(tmp)
	assert.True(t, os.IsNotExist(err), "temp file should not remain after a successful write")
}

func TestWriteFile_AtomicallyReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "state.json")
	tmp := final + ".tmp"

	require.NoError(t, durablefile.WriteFile(tmp, final, []byte("first"), 0o600))
	require.NoError(t, durablefile.WriteFile(tmp, final, []byte("second"), 0o600))

	got, err := os.ReadFile(final)
	require.NoError(t, err)
	assert.Equal(t, "second", string(got))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no leftover temp file should remain")
}

// downloader/installer가 쓰는 스트리밍 패턴: 직접 연 파일에 쓰고
// SyncAndClose로 내구성 있게 닫은 뒤 Rename으로 확정한다.
func TestSyncAndCloseThenRename_StreamingPattern(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "firmware.bin")
	tmp := final + ".tmp"

	f, err := os.Create(tmp)
	require.NoError(t, err)
	_, err = f.Write([]byte("firmware-bytes"))
	require.NoError(t, err)

	require.NoError(t, durablefile.SyncAndClose(f))
	require.NoError(t, durablefile.Rename(tmp, final))

	got, err := os.ReadFile(final)
	require.NoError(t, err)
	assert.Equal(t, "firmware-bytes", string(got))

	_, err = os.Stat(tmp)
	assert.True(t, os.IsNotExist(err), "temp file should not remain after a successful rename")
}

func TestRename_FailsWhenTempFileMissing(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "firmware.bin")
	missingTmp := filepath.Join(dir, "does-not-exist.tmp")

	err := durablefile.Rename(missingTmp, final)

	require.Error(t, err)
	assert.NoFileExists(t, final)
}

func TestWriteFile_FailsWhenDirectoryMissing(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "missing-dir", "state.json")
	tmp := final + ".tmp"

	err := durablefile.WriteFile(tmp, final, []byte("hello"), 0o600)

	require.Error(t, err)
}
