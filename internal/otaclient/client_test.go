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

func writeJSONError(w http.ResponseWriter, statusCode int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": code})
}

// VEHICLE-REQ-002
func TestClient_Register_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/vehicles/register", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer enroll-key", r.Header.Get("Authorization"))

		var body otaclient.RegisterRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "car-0001", body.VehicleID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(otaclient.RegisterResponse{VehicleToken: "token-abc"})
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
}

// VEHICLE-REQ-002
func TestClient_Register_InvalidEnrollmentKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusUnauthorized, otaclient.CodeInvalidEnrollmentKey)
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	_, err := client.Register(context.Background(), "wrong-key", otaclient.RegisterRequest{VehicleID: "car-0001"})

	require.Error(t, err)
	var apiErr *otaclient.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.Equal(t, otaclient.CodeInvalidEnrollmentKey, apiErr.Code)
}

// VEHICLE-REQ-001
func TestClient_CheckIn_UpdateTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/vehicles/car-0001/check-in", r.URL.Path)
		assert.Equal(t, "Bearer vehicle-token", r.Header.Get("Authorization"))

		var body otaclient.CheckInRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "1.0.0", body.CurrentVersion)
		require.NotNil(t, body.LastUpdate)
		assert.Equal(t, "cmp-1", body.LastUpdate.CampaignID)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(otaclient.CheckInResponse{
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
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Nil(t, body.LastUpdate)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(otaclient.CheckInResponse{UpdateTarget: false, NextCheckInSeconds: 30})
	}))
	defer server.Close()

	client := otaclient.New(server.URL, server.Client())
	resp, err := client.CheckIn(context.Background(), "car-0001", "vehicle-token", otaclient.CheckInRequest{
		CurrentVersion: "1.0.0",
	})

	require.NoError(t, err)
	assert.False(t, resp.UpdateTarget)
}

// VEHICLE-REQ-001
func TestClient_CheckIn_Errors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       string
		wantCode   string
	}{
		{name: "401 UNAUTHORIZED", statusCode: http.StatusUnauthorized, code: otaclient.CodeUnauthorized, wantCode: otaclient.CodeUnauthorized},
		{name: "403 VEHICLE_MISMATCH", statusCode: http.StatusForbidden, code: otaclient.CodeVehicleMismatch, wantCode: otaclient.CodeVehicleMismatch},
		{name: "500 코드 없음", statusCode: http.StatusInternalServerError, code: "", wantCode: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.code == "" {
					w.WriteHeader(tt.statusCode)
					return
				}
				writeJSONError(w, tt.statusCode, tt.code)
			}))
			defer server.Close()

			client := otaclient.New(server.URL, server.Client())
			_, err := client.CheckIn(context.Background(), "car-0001", "vehicle-token", otaclient.CheckInRequest{CurrentVersion: "1.0.0"})

			require.Error(t, err)
			var apiErr *otaclient.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.statusCode, apiErr.StatusCode)
			assert.Equal(t, tt.wantCode, apiErr.Code)
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

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(otaclient.Manifest{
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
func TestClient_GetManifest_Errors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		code       string
	}{
		{name: "404 CAMPAIGN_NOT_FOUND", statusCode: http.StatusNotFound, code: otaclient.CodeCampaignNotFound},
		{name: "409 NOT_UPDATE_TARGET", statusCode: http.StatusConflict, code: otaclient.CodeNotUpdateTarget},
		{name: "410 CAMPAIGN_INACTIVE", statusCode: http.StatusGone, code: otaclient.CodeCampaignInactive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSONError(w, tt.statusCode, tt.code)
			}))
			defer server.Close()

			client := otaclient.New(server.URL, server.Client())
			_, err := client.GetManifest(context.Background(), "car-0001", "cmp-1", "vehicle-token")

			require.Error(t, err)
			var apiErr *otaclient.APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.statusCode, apiErr.StatusCode)
			assert.Equal(t, tt.code, apiErr.Code)
		})
	}
}
