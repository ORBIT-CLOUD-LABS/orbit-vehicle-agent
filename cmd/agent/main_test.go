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

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
)

// TestRun_EndToEnd drives the real cmd/agent entry point — config loading,
// component assembly (state store, OTA client, downloader, installer,
// state machine) and the register/check-in/update loop — against httptest
// doubles for the OTA server and the firmware CDN. It covers the full
// docs/flow.md path: registration (③), a check-in that targets an update
// (④), fetching the manifest (⑤), downloading and verifying the firmware
// (⑥), installing it (⑦), and reporting the result on the next check-in
// (④), after which the server acknowledges it and the loop stops.
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var checkInCalls atomic.Int32

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/vehicles/register", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+enrollKey, r.Header.Get("Authorization"))
		writeJSON(t, w, otaclient.RegisterResponse{VehicleToken: "tok-1"})
	})

	mux.HandleFunc("POST /api/v1/vehicles/"+vehicleID+"/check-in", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))

		var req otaclient.CheckInRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		switch checkInCalls.Add(1) {
		case 1:
			require.Equal(t, "1.0.0", req.CurrentVersion)
			require.Nil(t, req.LastUpdate, "최초 체크인에는 보고할 이전 결과가 없어야 한다")
			writeJSON(t, w, otaclient.CheckInResponse{UpdateTarget: true, CampaignID: campaignID, NextCheckInSeconds: 0})
		case 2:
			require.Equal(t, "1.1.0", req.CurrentVersion, "설치 후 체크인은 새 버전을 보고해야 한다")
			require.NotNil(t, req.LastUpdate, "설치 결과는 다음 체크인에 보고되어야 한다")
			require.Equal(t, campaignID, req.LastUpdate.CampaignID)
			require.Equal(t, "SUCCEEDED", req.LastUpdate.Result)
			writeJSON(t, w, otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 0})
			cancel()
		default:
			writeJSON(t, w, otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 0})
		}
	})

	mux.HandleFunc("GET /api/v1/vehicles/"+vehicleID+"/campaigns/"+campaignID+"/manifest", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))
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
	t.Setenv("CHECKIN_INTERVAL", "30")
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("INITIAL_VERSION", "1.0.0")

	err := run(ctx, zaptest.NewLogger(t))
	require.NoError(t, err, "종료 신호(컨텍스트 취소)로 끝난 정상 종료는 에러를 반환하지 않아야 한다")

	require.Equal(t, int32(2), checkInCalls.Load())

	installed, err := os.ReadFile(filepath.Join(dataDir, "firmware", "current.bin"))
	require.NoError(t, err)
	require.Equal(t, firmware, string(installed))
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}
