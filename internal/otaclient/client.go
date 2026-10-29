// otaclient는 OTA 서버의 등록, 체크인 및 매니페스트 API(cmd/agent/main_test.go ③④⑤)에 대한 차량 에이전트의 HTTP 클라이언트입니다.

package otaclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
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

// RegisterResponse는 차량 등록 응답 본문입니다. VehicleToken은 이 호출에서만 반환되며, 에이전트는 메모리에만 보관합니다.
//
// flow.md는 응답 본문 필드 이름을 정확히 지정하지 않고 한 번 토큰이 반환된다고만 명시합니다. VehicleToken은 나머지 API와 일치하는 camelCase를 가정합니다. OTA 서버가 존재하면 확인하십시오.

type RegisterResponse struct {
	VehicleToken string `json:"vehicleToken"`
}

// OTA 서버는 차량이 마지막으로 시도한 업데이트의 결과를 보고하도록 요구합니다. LastUpdate는 CheckInRequest에 포함됩니다.
func (c *Client) Register(ctx context.Context, enrollmentKey string, req RegisterRequest) (RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/vehicles/register", enrollmentKey, req, &resp); err != nil {
		return RegisterResponse{}, err
	}
	return resp, nil
}

// LastUpdate는 마지막으로 시도한 업데이트의 결과를 보고합니다.
type LastUpdate struct {
	CampaignID    string    `json:"campaignId"`
	Result        string    `json:"result"`
	FailureReason string    `json:"failureReason,omitempty"`
	FailureDetail string    `json:"failureDetail,omitempty"`
	FinishedAt    time.Time `json:"finishedAt"`
}

// CheckInRequest는 주기적인 체크인 요청 본문입니다(VEHICLE-REQ-001, UPDATE-REQ-001). LastUpdate는 보고할 결과가 없으면 nil입니다.
type CheckInRequest struct {
	CurrentVersion string      `json:"currentVersion"`
	LastUpdate     *LastUpdate `json:"lastUpdate"`
}

// CheckInResponse는 차량이 업데이트 대상인지, 언제 다시 체크인해야 하는지 알려줍니다.
type CheckInResponse struct {
	UpdateTarget       bool   `json:"updateTarget"`
	CampaignID         string `json:"campaignId,omitempty"`
	NextCheckInSeconds int    `json:"nextCheckInSeconds"`
}

// CheckIn은 현재 버전과, 있다면 마지막 업데이트의 결과를 보고하고, 차량이 지금 매니페스트를 가져와야 하는지 여부를 반환합니다.
// 호출자는 CodeUnauthorized를 "재등록 및 재시도"로, 500 응답(Code가 비어 있음)을 "다음 체크인 주기에 재시도"로 처리해야 합니다.

func (c *Client) CheckIn(ctx context.Context, vehicleID, vehicleToken string, req CheckInRequest) (CheckInResponse, error) {
	var resp CheckInResponse
	path := fmt.Sprintf("/api/v1/vehicles/%s/check-in", url.PathEscape(vehicleID))
	if err := c.do(ctx, http.MethodPost, path, vehicleToken, req, &resp); err != nil {
		return CheckInResponse{}, err
	}
	return resp, nil
}

// manifest는 캠페인을 위해 다운로드한 펌웨어를 설명합니다(MANIFEST-REQ-001).
type Manifest struct {
	CampaignID    string    `json:"campaignId"`
	TargetVersion string    `json:"targetVersion"`
	FileSize      int64     `json:"fileSize"`
	SHA256        string    `json:"sha256"`
	DownloadURL   string    `json:"downloadUrl"`
	URLExpiresAt  time.Time `json:"urlExpiresAt"`
}

// GetManifest는 campaignID에 대한 다운로드 매니페스트를 가져옵니다. 
// 서버는 이 호출에서 대상 적격성을 다시 확인하므로, 호출자는 CodeNotUpdateTarget 및 CodeCampaignInactive를 "IDLE로 돌아가기"로 처리해야 하며 실패로 간주하지 않아야 합니다.

func (c *Client) GetManifest(ctx context.Context, vehicleID, campaignID, vehicleToken string) (Manifest, error) {
	var resp Manifest
	path := fmt.Sprintf("/api/v1/vehicles/%s/campaigns/%s/manifest", url.PathEscape(vehicleID), url.PathEscape(campaignID))
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
