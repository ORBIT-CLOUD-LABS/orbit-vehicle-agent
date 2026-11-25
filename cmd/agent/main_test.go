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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/config"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/firmwaresignature"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
)

// TestRun_EndToEnd는 OTA 서버와 펌웨어 CDN을 httptest 대역으로 두고 실제
// cmd/agent 진입점(설정 로드, 구성요소 조립, 등록·체크인·업데이트 루프)을
// 구동한다. docs/flow.md의 전체 경로를 다룬다: 등록(③), 업데이트 대상을
// 알려 주는 체크인(④), 매니페스트 조회(⑤), 펌웨어 다운로드·검증(⑥),
// 설치(⑦), 다음 체크인의 결과 보고(④). 서버가 결과를 확인하면 루프를 멈춘다.
func TestRun_EndToEnd(t *testing.T) {
	t.Run("verified firmware installs with configured location", func(t *testing.T) {
		testRunEndToEnd(t, true, "", "37.5665", "126.978")
	})
	t.Run("unknown key fails with embedded verifier and no location", func(t *testing.T) {
		testRunEndToEnd(t, false, "", "", "")
	})
	t.Run("wander position changes across check-ins", func(t *testing.T) {
		testRunEndToEnd(t, true, config.LocationWander, "35.18", "129.07")
	})
	t.Run("route position changes across check-ins", func(t *testing.T) {
		testRunEndToEnd(t, true, config.LocationRoute, "", "")
	})
}

func testRunEndToEnd(t *testing.T, acceptsSignature bool, mode config.LocationMode, latitude, longitude string) {
	t.Helper()
	const (
		vehicleID        = "car-0001"
		enrollKey        = "enrollment-secret"
		campaignID int64 = 7
		firmware         = "verified-firmware-bytes"
		keyID            = "end-to-end-test-key"
		signature        = "test-signature"
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
	var lastLocation atomic.Pointer[otaclient.Location]

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/vehicles/register", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+enrollKey, r.Header.Get("Authorization"))
		writeOTASuccess(t, w, http.StatusCreated, "CREATED", otaclient.RegisterResponse{VehicleToken: "tok-1"})
	})

	mux.HandleFunc("POST /api/v1/vehicles/"+vehicleID+"/check-in", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))

		var req struct {
			otaclient.CheckInRequest
			Location json.RawMessage `json:"location"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if mode == config.LocationWander || mode == config.LocationRoute {
			var got *otaclient.Location
			if assert.NoError(t, json.Unmarshal(req.Location, &got)) && assert.NotNil(t, got) {
				if mode == config.LocationWander {
					assert.InDelta(t, 35.18, got.Latitude, 0.02)
					assert.InDelta(t, 129.07, got.Longitude, 0.02)
				} else {
					// car-0001은 전체 경로의 약 41%인 대전 부근에서 출발한다.
					assert.InDelta(t, 36.35, got.Latitude, 0.02)
					assert.InDelta(t, 127.38, got.Longitude, 0.02)
				}
				if previous := lastLocation.Swap(got); previous != nil {
					assert.NotEqual(t, *previous, *got)
				}
			}
		} else if latitude == "" && longitude == "" {
			assert.Nil(t, req.Location)
		} else {
			assert.JSONEq(t, `{"latitude":`+latitude+`,"longitude":`+longitude+`}`, string(req.Location))
		}

		switch checkInCalls.Add(1) {
		case 1:
			assert.Equal(t, "1.0.0", req.CurrentVersion)
			assert.Nil(t, req.LastUpdate, "최초 체크인에는 보고할 이전 결과가 없어야 한다")
			writeOTASuccess(t, w, http.StatusOK, "OK", otaclient.CheckInResponse{UpdateTarget: true, CampaignID: campaignID, NextCheckInSeconds: 0})
		case 2:
			if assert.NotNil(t, req.LastUpdate, "설치 결과는 다음 체크인에 보고되어야 한다") {
				assert.Equal(t, campaignID, req.LastUpdate.CampaignID)
				if acceptsSignature {
					assert.Equal(t, "1.1.0", req.CurrentVersion)
					assert.Equal(t, "SUCCEEDED", req.LastUpdate.Result)
				} else {
					assert.Equal(t, "1.0.0", req.CurrentVersion)
					assert.Equal(t, "FAILED", req.LastUpdate.Result)
					assert.Equal(t, "SIGNATURE_INVALID", req.LastUpdate.FailureReason)
					assert.Equal(t, "unknown keyId: "+keyID, req.LastUpdate.FailureDetail)
				}
			}
			writeOTASuccess(t, w, http.StatusOK, "OK", otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 0})
			cancel()
		default:
			writeOTASuccess(t, w, http.StatusOK, "OK", otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 0})
		}
	})

	mux.HandleFunc("GET /api/v1/vehicles/"+vehicleID+"/campaigns/"+strconv.FormatInt(campaignID, 10)+"/manifest", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))
		writeOTASuccess(t, w, http.StatusOK, "OK", otaclient.Manifest{
			CampaignID:    campaignID,
			TargetVersion: "1.1.0",
			FileSize:      int64(len(firmware)),
			SHA256:        strings.ToUpper(firmwareSHA256),
			DownloadURL:   cdn.URL,
			KeyID:         keyID,
			Signature:     signature,
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
	t.Setenv("LATITUDE", latitude)
	t.Setenv("LONGITUDE", longitude)
	t.Setenv("LOCATION_MODE", string(mode))
	t.Setenv("WANDER_RADIUS_KM", "")
	t.Setenv("ROUTE_SPEED_KMH", "")

	runAgent := run
	var verifyCalls int
	if acceptsSignature {
		runAgent = func(ctx context.Context, logger *zap.Logger) error {
			return runWithVerifier(ctx, logger, testSignatureVerifier(func(gotKeyID, gotSignature string, metadata firmwaresignature.Metadata) error {
				verifyCalls++
				assert.Equal(t, keyID, gotKeyID)
				assert.Equal(t, signature, gotSignature)
				assert.Equal(t, firmwaresignature.Metadata{Model: "model-x", Version: "1.1.0", SHA256: firmwareSHA256, Size: int64(len(firmware))}, metadata)
				return nil
			}))
		}
	}
	err := runAgent(ctx, zaptest.NewLogger(t))
	require.NoError(t, err, "종료 신호(컨텍스트 취소)로 끝난 정상 종료는 에러를 반환하지 않아야 한다")

	require.Equal(t, int32(2), checkInCalls.Load())

	installedPath := filepath.Join(dataDir, "firmware", "current.bin")
	if !acceptsSignature {
		assert.NoFileExists(t, installedPath)
		return
	}
	assert.Equal(t, 1, verifyCalls)
	installed, err := os.ReadFile(installedPath)
	require.NoError(t, err)
	require.Equal(t, firmware, string(installed))
}

type testSignatureVerifier func(string, string, firmwaresignature.Metadata) error

func (testSignatureVerifier) HasKey(string) bool { return true }

func (verify testSignatureVerifier) Verify(keyID, signature string, metadata firmwaresignature.Metadata) error {
	return verify(keyID, signature, metadata)
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

func writeOTASuccess(t *testing.T, w http.ResponseWriter, statusCode int, code string, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"isSuccess": true,
		"code":      code,
		"message":   "요청에 성공했습니다.",
		"data":      data,
		"error":     nil,
	}))
}
