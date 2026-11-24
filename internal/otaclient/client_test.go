package otaclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
)

func writeAPIResponse(t *testing.T, w http.ResponseWriter, statusCode int, code, message string, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"isSuccess": statusCode < http.StatusBadRequest,
		"code":      code,
		"message":   message,
		"data":      data,
		"error":     nil,
	}))
}

// VEHICLE-REQ-002
func TestClient_Register_Success(t *testing.T) {
	tests := []struct {
		code       string
		statusCode int
		message    string
	}{
		{code: "CREATED", statusCode: http.StatusCreated, message: "리소스를 생성했습니다."},
		{code: "OK", statusCode: http.StatusOK, message: "요청에 성공했습니다."},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v1/vehicles/register", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "Bearer enroll-key", r.Header.Get("Authorization"))

				var body otaclient.RegisterRequest
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, "car-0001", body.VehicleID)

				writeAPIResponse(t, w, tt.statusCode, tt.code, tt.message, otaclient.RegisterResponse{VehicleToken: "token-abc"})
			}))
			defer server.Close()

			client := otaclient.New(server.URL, server.Client())
			resp, err := client.Register(context.Background(), "enroll-key", otaclient.RegisterRequest{
				VehicleID:      "car-0001",
				Model:          "model-x",
				HWVersion:      "hw-2",
				Region:         "kr",
				CurrentVersion: "1.0.0",
			})

			require.NoError(t, err)
			assert.Equal(t, "token-abc", resp.VehicleToken)
		})
	}
}

// VEHICLE-REQ-002
func TestClient_Register_InvalidSuccessData(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "빈 본문"},
		{name: "잘못된 JSON", body: `{"data":`},
		{name: "data 누락", body: `{"isSuccess":true,"code":"OK","vehicleToken":"token-abc"}`},
		{name: "data null", body: `{"isSuccess":true,"code":"OK","data":null}`},
		{name: "data 타입 불일치", body: `{"isSuccess":true,"code":"OK","data":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := w.Write([]byte(tt.body))
				assert.NoError(t, err)
			}))
			defer server.Close()

			client := otaclient.New(server.URL, server.Client())
			_, err := client.Register(context.Background(), "enroll-key", otaclient.RegisterRequest{VehicleID: "car-0001"})

			require.Error(t, err)
		})
	}
}

// VEHICLE-REQ-002
func TestClient_Register_InvalidEnrollmentKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIResponse(t, w, http.StatusUnauthorized, otaclient.CodeInvalidEnrollmentKey, "등록 키가 올바르지 않습니다.", nil)
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	_, err := client.Register(context.Background(), "wrong-key", otaclient.RegisterRequest{VehicleID: "car-0001"})

	require.Error(t, err)
	var apiErr *otaclient.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.Equal(t, otaclient.CodeInvalidEnrollmentKey, apiErr.Code)
	assert.Equal(t, "등록 키가 올바르지 않습니다.", apiErr.Message)
}

// VEHICLE-REQ-001
func TestClient_CheckIn_UpdateTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/vehicles/car-0001/check-in", r.URL.Path)
		assert.Equal(t, "Bearer vehicle-token", r.Header.Get("Authorization"))

		var body otaclient.CheckInRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "1.0.0", body.CurrentVersion)
		if assert.NotNil(t, body.LastUpdate) {
			assert.Equal(t, "cmp-1", body.LastUpdate.CampaignID)
		}

		writeAPIResponse(t, w, http.StatusOK, "OK", "요청에 성공했습니다.", otaclient.CheckInResponse{
			UpdateTarget:       true,
			CampaignID:         "cmp-2",
			NextCheckInSeconds: 30,
		})
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	resp, err := client.CheckIn(context.Background(), "car-0001", "vehicle-token", otaclient.CheckInRequest{
		CurrentVersion: "1.0.0",
		LastUpdate: &otaclient.LastUpdate{
			CampaignID: "cmp-1",
			Result:     "SUCCEEDED",
			FinishedAt: time.Now(),
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.UpdateTarget)
	assert.Equal(t, "cmp-2", resp.CampaignID)
	assert.Equal(t, 30, resp.NextCheckInSeconds)
}

// VEHICLE-REQ-001
func TestClient_CheckIn_NoUpdateTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body otaclient.CheckInRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Nil(t, body.LastUpdate)

		writeAPIResponse(t, w, http.StatusOK, "OK", "요청에 성공했습니다.", otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 30})
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	resp, err := client.CheckIn(context.Background(), "car-0001", "vehicle-token", otaclient.CheckInRequest{
		CurrentVersion: "1.0.0",
	})

	require.NoError(t, err)
	assert.False(t, resp.UpdateTarget)
	assert.Equal(t, 30, resp.NextCheckInSeconds)
}

// VEHICLE-REQ-001
func TestClient_CheckIn_Errors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       string
		message    string
	}{
		{name: "401 UNAUTHORIZED", statusCode: http.StatusUnauthorized, code: otaclient.CodeUnauthorized, message: "차량 토큰 인증에 실패했습니다."},
		{name: "403 VEHICLE_MISMATCH", statusCode: http.StatusForbidden, code: otaclient.CodeVehicleMismatch, message: "토큰의 차량과 요청 경로의 차량이 다릅니다."},
		{name: "500 코드 없음", statusCode: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.code == "" {
					w.WriteHeader(tt.statusCode)
					return
				}
				writeAPIResponse(t, w, tt.statusCode, tt.code, tt.message, nil)
			}))
			defer server.Close()

			client := otaclient.New(server.URL, server.Client())
			_, err := client.CheckIn(context.Background(), "car-0001", "vehicle-token", otaclient.CheckInRequest{CurrentVersion: "1.0.0"})

			require.Error(t, err)
			var apiErr *otaclient.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.statusCode, apiErr.StatusCode)
			assert.Equal(t, tt.code, apiErr.Code)
			assert.Equal(t, tt.message, apiErr.Message)
		})
	}
}

// MANIFEST-REQ-001
func TestClient_GetManifest_Success(t *testing.T) {
	expiresAt := time.Now().Add(10 * time.Minute).Truncate(time.Second).UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/vehicles/car-0001/campaigns/cmp-1/manifest", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer vehicle-token", r.Header.Get("Authorization"))

		writeAPIResponse(t, w, http.StatusOK, "OK", "요청에 성공했습니다.", otaclient.Manifest{
			CampaignID:    "cmp-1",
			TargetVersion: "1.1.0",
			FileSize:      1024,
			SHA256:        "abc123",
			DownloadURL:   "https://cdn.example.com/firmware.bin?md5=x&expires=1",
			URLExpiresAt:  expiresAt,
		})
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	manifest, err := client.GetManifest(context.Background(), "car-0001", "cmp-1", "vehicle-token")

	require.NoError(t, err)
	assert.Equal(t, "cmp-1", manifest.CampaignID)
	assert.Equal(t, "1.1.0", manifest.TargetVersion)
	assert.Equal(t, int64(1024), manifest.FileSize)
	assert.Equal(t, "abc123", manifest.SHA256)
	assert.True(t, expiresAt.Equal(manifest.URLExpiresAt))
}

// MANIFEST-REQ-001
func TestClient_GetManifest_EscapesPathParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/vehicles/car-0001/campaigns/cmp%2F1%3Fx=y/manifest", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery)

		writeAPIResponse(t, w, http.StatusOK, "OK", "요청에 성공했습니다.", otaclient.Manifest{CampaignID: "cmp/1?x=y"})
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	_, err := client.GetManifest(context.Background(), "car-0001", "cmp/1?x=y", "vehicle-token")

	require.NoError(t, err)
}

// MANIFEST-REQ-001
func TestClient_GetManifest_Errors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       string
		message    string
	}{
		{name: "404 CAMPAIGN_NOT_FOUND", statusCode: http.StatusNotFound, code: otaclient.CodeCampaignNotFound, message: "캠페인을 찾을 수 없습니다."},
		{name: "409 NOT_UPDATE_TARGET", statusCode: http.StatusConflict, code: otaclient.CodeNotUpdateTarget, message: "업데이트 대상 차량이 아닙니다."},
		{name: "410 CAMPAIGN_INACTIVE", statusCode: http.StatusGone, code: otaclient.CodeCampaignInactive, message: "캠페인이 활성 상태가 아닙니다."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeAPIResponse(t, w, tt.statusCode, tt.code, tt.message, nil)
			}))
			defer server.Close()

			client := otaclient.New(server.URL, server.Client())
			_, err := client.GetManifest(context.Background(), "car-0001", "cmp-1", "vehicle-token")

			require.Error(t, err)
			var apiErr *otaclient.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.statusCode, apiErr.StatusCode)
			assert.Equal(t, tt.code, apiErr.Code)
			assert.Equal(t, tt.message, apiErr.Message)
		})
	}
}
