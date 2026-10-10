// Package otaclient is the vehicle agent's HTTP client for the OTA server's
// registration, check-in, and manifest APIs (docs/flow.md ③④⑤ in the orbit
// repo).
package otaclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Error codes the OTA server returns, used to classify APIError.
const (
	CodeInvalidEnrollmentKey = "INVALID_ENROLLMENT_KEY"
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeVehicleMismatch      = "VEHICLE_MISMATCH"
	CodeCampaignNotFound     = "CAMPAIGN_NOT_FOUND"
	CodeNotUpdateTarget      = "NOT_UPDATE_TARGET"
	CodeCampaignInactive     = "CAMPAIGN_INACTIVE"
)

// APIError is an error response from the OTA server. Callers branch on
// StatusCode or Code to decide how to react (re-register, retry next cycle,
// request a new manifest, and so on).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("ota server: http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("ota server: %s (http %d): %s", e.Code, e.StatusCode, e.Message)
}

// Client calls the OTA server's vehicle-facing APIs.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a Client that calls baseURL. If httpClient is nil,
// http.DefaultClient is used.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}
}

// RegisterRequest is the vehicle registration request body (VEHICLE-REQ-002).
type RegisterRequest struct {
	VehicleID      string `json:"vehicleId"`
	Model          string `json:"model"`
	HWVersion      string `json:"hwVersion"`
	Region         string `json:"region"`
	CurrentVersion string `json:"currentVersion"`
}

// RegisterResponse is the vehicle registration response body. VehicleToken
// is returned only on this call; the agent keeps it in memory only.
//
// flow.md does not give the response body's exact field names, only that a
// token is returned once. VehicleToken assumes camelCase matching the rest
// of the API; confirm against the OTA server once it exists.
type RegisterResponse struct {
	VehicleToken string `json:"vehicleToken"`
}

// Register enrolls the vehicle with the OTA server using the shared
// enrollment key and returns a freshly issued vehicle token.
func (c *Client) Register(ctx context.Context, enrollmentKey string, req RegisterRequest) (RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/vehicles/register", enrollmentKey, req, &resp); err != nil {
		return RegisterResponse{}, err
	}
	return resp, nil
}

// LastUpdate reports the outcome of the most recently attempted update.
type LastUpdate struct {
	CampaignID    string    `json:"campaignId"`
	Result        string    `json:"result"`
	FailureReason string    `json:"failureReason,omitempty"`
	FailureDetail string    `json:"failureDetail,omitempty"`
	FinishedAt    time.Time `json:"finishedAt"`
}

// CheckInRequest is the periodic check-in request body (VEHICLE-REQ-001,
// UPDATE-REQ-001). LastUpdate is nil when there is no result to report.
type CheckInRequest struct {
	CurrentVersion string      `json:"currentVersion"`
	LastUpdate     *LastUpdate `json:"lastUpdate"`
}

// CheckInResponse tells the vehicle whether it is an update target and when
// to check in again.
type CheckInResponse struct {
	UpdateTarget       bool   `json:"updateTarget"`
	CampaignID         string `json:"campaignId,omitempty"`
	NextCheckInSeconds int    `json:"nextCheckInSeconds"`
}

// CheckIn reports the current version and, if any, the last update's
// result, and returns whether the vehicle should fetch a manifest now.
//
// Callers should treat CodeUnauthorized as "re-register and retry", and any
// 500 response (Code is empty) as "retry on the next check-in cycle".
func (c *Client) CheckIn(ctx context.Context, vehicleID, vehicleToken string, req CheckInRequest) (CheckInResponse, error) {
	var resp CheckInResponse
	path := fmt.Sprintf("/api/v1/vehicles/%s/check-in", vehicleID)
	if err := c.do(ctx, http.MethodPost, path, vehicleToken, req, &resp); err != nil {
		return CheckInResponse{}, err
	}
	return resp, nil
}

// Manifest describes the firmware to download for a campaign
// (MANIFEST-REQ-001).
type Manifest struct {
	CampaignID    string    `json:"campaignId"`
	TargetVersion string    `json:"targetVersion"`
	FileSize      int64     `json:"fileSize"`
	SHA256        string    `json:"sha256"`
	DownloadURL   string    `json:"downloadUrl"`
	URLExpiresAt  time.Time `json:"urlExpiresAt"`
}

// GetManifest fetches the download manifest for campaignID. The server
// re-checks target eligibility on this call, so callers must treat
// CodeNotUpdateTarget and CodeCampaignInactive as "go back to IDLE", not as
// failures.
func (c *Client) GetManifest(ctx context.Context, vehicleID, campaignID, vehicleToken string) (Manifest, error) {
	var resp Manifest
	path := fmt.Sprintf("/api/v1/vehicles/%s/campaigns/%s/manifest", vehicleID, campaignID)
	if err := c.do(ctx, http.MethodGet, path, vehicleToken, nil, &resp); err != nil {
		return Manifest{}, err
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path, bearerToken string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call ota server: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return parseAPIError(resp.StatusCode, respBody)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}
	return nil
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func parseAPIError(statusCode int, body []byte) error {
	var parsed errorBody
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Code == "" {
		return &APIError{StatusCode: statusCode, Message: string(body)}
	}
	return &APIError{StatusCode: statusCode, Code: parsed.Code, Message: parsed.Message}
}
