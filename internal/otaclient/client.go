// otaclient는 OTA 서버의 등록, 체크인 및 매니페스트 API(cmd/agent/main_test.go ③④⑤)에 대한 차량 에이전트의 HTTP 클라이언트입니다.

package otaclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxResponseBodyBytes = 64 << 10
	maxErrorMessageBytes = 512
)

// OTA 서버가 반환하는 오류 코드, APIError를 분류하는 데 사용됩니다.
const (
	CodeInvalidEnrollmentKey = "INVALID_ENROLLMENT_KEY"
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeVehicleMismatch      = "VEHICLE_MISMATCH"
	CodeCampaignNotFound     = "CAMPAIGN_NOT_FOUND"
	CodeNotUpdateTarget      = "NOT_UPDATE_TARGET"
	CodeCampaignInactive     = "CAMPAIGN_INACTIVE"
)

// APIError는 OTA 서버의 오류 응답입니다. 호출자는 StatusCode 또는 Code를 기준으로 재등록, 다음 주기 재시도, 새 매니페스트 요청 등을 결정합니다.
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

// Client는 OTA 서버의 차량 API를 호출합니다.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient는 baseURL을 호출하는 Client를 반환합니다. httpClient가 nil이면 http.DefaultClient가 사용됩니다.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}
}

// RegisterRequest는 차량 등록 요청 본문입니다(VEHICLE-REQ-002).
type RegisterRequest struct {
	VehicleID      string `json:"vehicleId"`
	Model          string `json:"model"`
	HWVersion      string `json:"hwVersion"`
	Region         string `json:"region"`
	CurrentVersion string `json:"currentVersion"`
}

// RegisterResponse는 차량 등록 성공 응답의 data입니다(orbit의 docs/api-spec.md §2.1).
// 서버는 등록과 재등록 시 data.vehicleToken을 반환하고, 에이전트는 이를 메모리에만 보관합니다.
type RegisterResponse struct {
	VehicleToken string `json:"vehicleToken"`
}

// Register는 차량을 등록하거나 재등록하고 차량 토큰을 받습니다(VEHICLE-REQ-002).
// 등록 키가 틀리면 CodeInvalidEnrollmentKey를 담은 *APIError를 반환하며, 호출자는 재시도하지 않고 시작 실패로 처리해야 합니다.
func (c *Client) Register(ctx context.Context, enrollmentKey string, req RegisterRequest) (RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/vehicles/register", enrollmentKey, req, &resp); err != nil {
		return RegisterResponse{}, err
	}
	return resp, nil
}

// LastUpdate는 마지막으로 시도한 업데이트의 결과를 보고합니다.
type LastUpdate struct {
	CampaignID    int64     `json:"campaignId"`
	Result        string    `json:"result"`
	FailureReason string    `json:"failureReason,omitempty"`
	FailureDetail string    `json:"failureDetail,omitempty"`
	EdgeID        string    `json:"edgeId,omitempty"`
	FinishedAt    time.Time `json:"finishedAt"`
}

// Location은 체크인에서 보고하는 차량의 위도와 경도입니다.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// CheckInRequest는 주기적인 체크인 요청 본문입니다(VEHICLE-REQ-001, UPDATE-REQ-001).
// Location과 LastUpdate는 각각 보고할 위치와 업데이트 결과가 없으면 nil입니다.
type CheckInRequest struct {
	CurrentVersion string      `json:"currentVersion"`
	Location       *Location   `json:"location,omitempty"`
	LastUpdate     *LastUpdate `json:"lastUpdate"`
}

// CheckInResponse는 차량이 업데이트 대상인지, 언제 다시 체크인해야 하는지 알려줍니다.
type CheckInResponse struct {
	UpdateTarget       bool  `json:"updateTarget"`
	CampaignID         int64 `json:"campaignId,omitempty"`
	NextCheckInSeconds int   `json:"nextCheckInSeconds"`
}

// CheckIn은 현재 버전과, 있다면 차량 위치와 마지막 업데이트 결과를 보고하고, 차량이 지금 매니페스트를 가져와야 하는지 여부를 반환합니다.
// 호출자는 CodeUnauthorized를 받으면 재등록 후 재시도하고, HTTP 500을 받으면 다음 체크인 주기에 재시도해야 합니다.
func (c *Client) CheckIn(ctx context.Context, vehicleID, vehicleToken string, req CheckInRequest) (CheckInResponse, error) {
	var resp CheckInResponse
	path := fmt.Sprintf("/api/v1/vehicles/%s/check-in", url.PathEscape(vehicleID))
	if err := c.do(ctx, http.MethodPost, path, vehicleToken, req, &resp); err != nil {
		return CheckInResponse{}, err
	}
	if resp.UpdateTarget && resp.CampaignID <= 0 {
		return CheckInResponse{}, fmt.Errorf("check-in target campaign id %d: must be positive", resp.CampaignID)
	}
	return resp, nil
}

// Manifest는 캠페인의 펌웨어와 다운로드 위치를 설명합니다(MANIFEST-REQ-001).
type Manifest struct {
	CampaignID    int64         `json:"campaignId"`
	TargetVersion string        `json:"targetVersion"`
	FileSize      int64         `json:"fileSize"`
	SHA256        string        `json:"sha256"`
	Edge          *ManifestEdge `json:"edge,omitempty"`
	DownloadURL   string        `json:"downloadUrl"`
	FallbackURLs  []FallbackURL `json:"fallbackUrls,omitempty"`
	URLExpiresAt  time.Time     `json:"urlExpiresAt"`
	Signature     string        `json:"signature,omitempty"`
	KeyID         string        `json:"keyId,omitempty"`
}

// ManifestEdge는 서버가 선택한 기본 CDN Edge입니다.
type ManifestEdge struct {
	EdgeID     string   `json:"edgeId"`
	DistanceKM *float64 `json:"distanceKm"` // 차량 위치를 모르면 nil입니다.
}

// FallbackURL은 대체 CDN Edge와 해당 다운로드 URL을 담습니다.
type FallbackURL struct {
	EdgeID string `json:"edgeId"`
	URL    string `json:"url"`
}

// GetManifest는 campaignID에 대한 다운로드 매니페스트를 가져옵니다.
// 서버는 이 호출에서 대상 적격성을 다시 확인하므로, 호출자는 CodeNotUpdateTarget 및 CodeCampaignInactive를 "IDLE로 돌아가기"로 처리해야 하며 실패로 간주하지 않아야 합니다.
func (c *Client) GetManifest(ctx context.Context, vehicleID string, campaignID int64, vehicleToken string) (Manifest, error) {
	var resp Manifest
	path := fmt.Sprintf("/api/v1/vehicles/%s/campaigns/%d/manifest", url.PathEscape(vehicleID), campaignID)
	if err := c.do(ctx, http.MethodGet, path, vehicleToken, nil, &resp); err != nil {
		return Manifest{}, err
	}
	if resp.CampaignID <= 0 {
		return Manifest{}, fmt.Errorf("manifest campaign id %d: must be positive", resp.CampaignID)
	}
	if resp.CampaignID != campaignID {
		return Manifest{}, fmt.Errorf("manifest campaign id %d does not match requested id %d", resp.CampaignID, campaignID)
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

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return parseAPIError(resp.StatusCode, respBody)
	}
	if len(respBody) > maxResponseBodyBytes {
		return fmt.Errorf("ota response body exceeds %d bytes", maxResponseBodyBytes)
	}

	if out == nil {
		return nil
	}

	var envelope struct {
		Data *json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode response envelope: %w", err)
	}
	if envelope.Data == nil {
		return errors.New("ota server response is missing data")
	}
	if err := json.Unmarshal(*envelope.Data, out); err != nil {
		return fmt.Errorf("decode response data: %w", err)
	}
	return nil
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func parseAPIError(statusCode int, body []byte) error {
	var parsed errorBody
	if len(body) <= maxResponseBodyBytes && json.Unmarshal(body, &parsed) == nil && parsed.Code != "" {
		return &APIError{StatusCode: statusCode, Code: parsed.Code, Message: truncateErrorMessage(parsed.Message)}
	}
	return &APIError{StatusCode: statusCode, Message: truncateErrorMessage(string(body))}
}

func truncateErrorMessage(message string) string {
	if len(message) <= maxErrorMessageBytes {
		return message
	}
	const suffix = "..."
	return strings.ToValidUTF8(message[:maxErrorMessageBytes-len(suffix)], "") + suffix
}
