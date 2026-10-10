package downloader_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DOWNLOAD-REQ-001
func TestDownloader_Download_Success(t *testing.T) {
	content := []byte("firmware-image-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dataDir := t.TempDir()
	d := downloader.New(dataDir, server.Client())

	result, err := d.Download(context.Background(), "campaign-1", server.URL, int64(len(content)), sha256Hex(content))
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dataDir, "downloads", "campaign-1.bin"), result.Path)
	assert.Equal(t, int64(len(content)), result.Size)

	written, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	assert.Equal(t, content, written)

	_, err = os.Stat(result.Path + ".tmp")
	assert.True(t, os.IsNotExist(err), "temp file should not remain after a successful download")
}

// DOWNLOAD-REQ-001
func TestDownloader_Download_SizeMismatch(t *testing.T) {
	content := []byte("firmware-image-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dataDir := t.TempDir()
	d := downloader.New(dataDir, server.Client())

	_, err := d.Download(context.Background(), "campaign-1", server.URL, int64(len(content))+1, sha256Hex(content))
	require.Error(t, err)

	var downloadErr *downloader.Error
	require.ErrorAs(t, err, &downloadErr)
	assert.Equal(t, downloader.FailureDownloadFailed, downloadErr.Reason)

	assertNoFilesLeft(t, dataDir, "campaign-1")
}

// DOWNLOAD-REQ-001
func TestDownloader_Download_HashMismatch(t *testing.T) {
	content := []byte("firmware-image-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dataDir := t.TempDir()
	d := downloader.New(dataDir, server.Client())

	_, err := d.Download(context.Background(), "campaign-1", server.URL, int64(len(content)), sha256Hex([]byte("different-content")))
	require.Error(t, err)

	var downloadErr *downloader.Error
	require.ErrorAs(t, err, &downloadErr)
	assert.Equal(t, downloader.FailureHashMismatch, downloadErr.Reason)

	assertNoFilesLeft(t, dataDir, "campaign-1")
}

// DOWNLOAD-REQ-001
func TestDownloader_Download_SignatureOrExpiredURL(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "403 서명 틀림", statusCode: http.StatusForbidden},
		{name: "410 URL 만료", statusCode: http.StatusGone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			dataDir := t.TempDir()
			d := downloader.New(dataDir, server.Client())

			_, err := d.Download(context.Background(), "campaign-1", server.URL, 10, "deadbeef")
			require.ErrorIs(t, err, downloader.ErrManifestExpired)
		})
	}
}

// DOWNLOAD-REQ-001
func TestDownloader_Download_OriginFailure(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "404 Origin에도 파일 없음", statusCode: http.StatusNotFound},
		{name: "502 CDN->Origin 오류", statusCode: http.StatusBadGateway},
		{name: "504 CDN->Origin 타임아웃", statusCode: http.StatusGatewayTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			dataDir := t.TempDir()
			d := downloader.New(dataDir, server.Client())

			_, err := d.Download(context.Background(), "campaign-1", server.URL, 10, "deadbeef")
			require.Error(t, err)

			var downloadErr *downloader.Error
			require.ErrorAs(t, err, &downloadErr)
			assert.Equal(t, downloader.FailureDownloadFailed, downloadErr.Reason)
		})
	}
}

// DOWNLOAD-REQ-001
func TestDownloader_Download_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := server.URL
	server.Close()

	dataDir := t.TempDir()
	d := downloader.New(dataDir, http.DefaultClient)

	_, err := d.Download(context.Background(), "campaign-1", unreachableURL, 10, "deadbeef")
	require.Error(t, err)

	var downloadErr *downloader.Error
	require.ErrorAs(t, err, &downloadErr)
	assert.Equal(t, downloader.FailureDownloadFailed, downloadErr.Reason)
}

func assertNoFilesLeft(t *testing.T, dataDir, campaignID string) {
	t.Helper()

	downloadsDir := filepath.Join(dataDir, "downloads")
	entries, err := os.ReadDir(downloadsDir)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	assert.Empty(t, entries, "no partial or stale files should remain after a failed %s download", campaignID)
}
