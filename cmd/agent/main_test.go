package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
)

// TestRun_EndToEnd는 OTA 서버와 펌웨어 CDN을 httptest 대역으로 두고 실제
// cmd/agent 진입점(설정 로드, 구성요소 조립, 등록·체크인·업데이트 루프)을
// 구동한다. docs/flow.md의 전체 경로를 다룬다: 등록(③), 업데이트 대상을
// 알려 주는 체크인(④), 매니페스트 조회(⑤), 펌웨어 다운로드·검증(⑥),
// 설치(⑦), 다음 체크인의 결과 보고(④). 서버가 결과를 확인하면 루프를 멈춘다.
func TestRun_EndToEnd(t *testing.T) {
	const (
		vehicleID  = "car-0001"
		enrollKey  = "enrollment-secret"
		campaignID = "cmp-1"
		firmware   = "verified-firmware-bytes"
	)
	sum := sha256.Sum256([]byte(firmware))
	firmwareSHA256 := hex.EncodeToString(sum[:])

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(firmware))
	}))
	defer cdn.Close()

	// CHECKIN_INTERVAL(1초)이 상태 머신에 연결되지 않으면 기본 간격(30초)을
	// 기다리다 이 제한에 걸린다.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var checkInCalls atomic.Int32

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/vehicles/register", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+enrollKey, r.Header.Get("Authorization"))
		writeJSON(t, w, otaclient.RegisterResponse{VehicleToken: "tok-1"})
	})

	mux.HandleFunc("POST /api/v1/vehicles/"+vehicleID+"/check-in", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))

		var req otaclient.CheckInRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		switch checkInCalls.Add(1) {
		case 1:
			assert.Equal(t, "1.0.0", req.CurrentVersion)
			assert.Nil(t, req.LastUpdate, "최초 체크인에는 보고할 이전 결과가 없어야 한다")
			writeJSON(t, w, otaclient.CheckInResponse{UpdateTarget: true, CampaignID: campaignID, NextCheckInSeconds: 0})
		case 2:
			assert.Equal(t, "1.1.0", req.CurrentVersion, "설치 후 체크인은 새 버전을 보고해야 한다")
			if assert.NotNil(t, req.LastUpdate, "설치 결과는 다음 체크인에 보고되어야 한다") {
				assert.Equal(t, campaignID, req.LastUpdate.CampaignID)
				assert.Equal(t, "SUCCEEDED", req.LastUpdate.Result)
			}
			writeJSON(t, w, otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 0})
			cancel()
		default:
			writeJSON(t, w, otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 0})
		}
	})

	mux.HandleFunc("GET /api/v1/vehicles/"+vehicleID+"/campaigns/"+campaignID+"/manifest", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))
		writeJSON(t, w, otaclient.Manifest{
			CampaignID:    campaignID,
			TargetVersion: "1.1.0",
			FileSize:      int64(len(firmware)),
			SHA256:        firmwareSHA256,
			DownloadURL:   cdn.URL,
		})
	})

	otaServer := httptest.NewServer(mux)
	defer otaServer.Close()

	dataDir := t.TempDir()
	t.Setenv("VEHICLE_ID", vehicleID)
	t.Setenv("VEHICLE_MODEL", "model-x")
	t.Setenv("HW_VERSION", "hw-1")
	t.Setenv("REGION", "kr")
	t.Setenv("ENROLLMENT_KEY", enrollKey)
	t.Setenv("OTA_SERVER_URL", otaServer.URL)
	t.Setenv("CHECKIN_INTERVAL", "1")
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("INITIAL_VERSION", "1.0.0")

	err := run(ctx, zaptest.NewLogger(t))
	require.NoError(t, err, "종료 신호(컨텍스트 취소)로 끝난 정상 종료는 에러를 반환하지 않아야 한다")

	require.Equal(t, int32(2), checkInCalls.Load())

	installed, err := os.ReadFile(filepath.Join(dataDir, "firmware", "current.bin"))
	require.NoError(t, err)
	require.Equal(t, firmware, string(installed))
}

// 서버나 CDN이 응답하지 않아도 체크인 루프가 멈추지 않도록 HTTP 요청에
// 시간 제한을 둔다.
func TestHTTPClients_HaveTimeouts(t *testing.T) {
	assert.Equal(t, otaRequestTimeout, newOTAHTTPClient().Timeout)

	cdn := newCDNHTTPClient()
	assert.Zero(t, cdn.Timeout, "펌웨어 본문 다운로드에는 전체 시간 제한을 두지 않는다")
	transport, ok := cdn.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, cdnResponseHeaderTimeout, transport.ResponseHeaderTimeout)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(t, json.NewEncoder(w).Encode(v))
}
