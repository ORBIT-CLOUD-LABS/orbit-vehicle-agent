package machine_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/machine"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

func TestMachine_Run_DownloadEdgePolicy(t *testing.T) {
	const (
		firmware = "firmware-image"
		timeout  = 0
	)
	now := time.Now()
	expired := now.Add(-time.Hour)
	valid := now.Add(time.Hour)
	tests := []struct {
		name             string
		edgeStatuses     [][]int
		freshEdges       []int
		initialExpiresAt time.Time
		freshExpiresAt   time.Time
		wantRequests     []int
		wantPaths        []string
		wantManifests    int
		wantResult       state.Result
		installErr       error
		hashMismatch     bool
	}{
		{
			name: "유효한 URL은 바로 요청", edgeStatuses: [][]int{{http.StatusOK}},
			initialExpiresAt: valid, wantRequests: []int{0}, wantPaths: []string{"/old"},
			wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "만료 시 갱신한 URL만 요청", edgeStatuses: [][]int{{http.StatusOK}},
			initialExpiresAt: expired, freshExpiresAt: valid, wantRequests: []int{0}, wantPaths: []string{"/new"},
			wantManifests: 2, wantResult: state.ResultSucceeded,
		},
		{
			name: "갱신한 URL도 만료됐으면 Edge 요청 없이 실패", edgeStatuses: [][]int{{http.StatusOK}},
			initialExpiresAt: expired, freshExpiresAt: expired,
			wantManifests: 2, wantResult: state.ResultFailed,
		},
		{
			name: "만료로 갱신한 뒤 410을 받아도 다시 갱신하지 않음", edgeStatuses: [][]int{{http.StatusGone}, {http.StatusOK}},
			initialExpiresAt: expired, freshExpiresAt: valid, wantRequests: []int{0}, wantPaths: []string{"/new"},
			wantManifests: 2, wantResult: state.ResultFailed,
		},
		{
			name: "502 후 같은 Edge에서 성공", edgeStatuses: [][]int{{http.StatusBadGateway, http.StatusOK}, {http.StatusOK}},
			wantRequests: []int{0, 0}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "504 재시도 초과 후 목록 순서대로 전환", edgeStatuses: [][]int{{http.StatusGatewayTimeout}, nil, {http.StatusOK}},
			wantRequests: []int{0, 0, 0, 1, 2}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name:         "503과 500은 같은 Edge 재시도 없이 전환",
			edgeStatuses: [][]int{{http.StatusServiceUnavailable}, {http.StatusInternalServerError}, {http.StatusOK}},
			wantRequests: []int{0, 1, 2}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "연결 실패 후 즉시 전환", edgeStatuses: [][]int{nil, {http.StatusOK}},
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "대체 Edge에서 받은 뒤 설치 실패에도 Edge를 보고", edgeStatuses: [][]int{nil, {http.StatusOK}},
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultFailed, installErr: errBoom,
		},
		{
			name: "해시 불일치에도 파일을 받은 대체 Edge를 보고", edgeStatuses: [][]int{nil, {http.StatusOK}, {http.StatusOK}},
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultFailed, hashMismatch: true,
		},
		{
			name: "타임아웃 후 즉시 전환", edgeStatuses: [][]int{{timeout}, {http.StatusOK}},
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "404는 즉시 실패", edgeStatuses: [][]int{{http.StatusNotFound}, {http.StatusOK}},
			wantRequests: []int{0}, wantManifests: 1, wantResult: state.ResultFailed,
		},
		{
			name: "대체 Edge의 404도 매니페스트 갱신 없이 종료", edgeStatuses: [][]int{nil, {http.StatusNotFound}, {http.StatusOK}},
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultFailed,
		},
		{
			name: "403은 같은 Edge에서 새 URL로 재시도", edgeStatuses: [][]int{{http.StatusForbidden, http.StatusOK}, {http.StatusOK}},
			wantRequests: []int{0, 0}, wantPaths: []string{"/old", "/new"}, wantManifests: 2, wantResult: state.ResultSucceeded,
		},
		{
			name: "410이 반복되면 전환 없이 실패", edgeStatuses: [][]int{{http.StatusGone}, {http.StatusOK}},
			wantRequests: []int{0, 0}, wantPaths: []string{"/old", "/new"}, wantManifests: 2, wantResult: state.ResultFailed,
		},
		{
			name: "모든 Edge 실패 후 새 목록의 대체 Edge에서 성공", edgeStatuses: [][]int{nil, nil, nil, {http.StatusOK}},
			freshEdges: []int{2, 3}, wantRequests: []int{0, 1, 2, 3}, wantPaths: []string{"/old", "/old-fallback", "/new", "/new-fallback"},
			wantManifests: 2, wantResult: state.ResultSucceeded,
		},
		{
			name: "새 목록에서도 모든 Edge가 실패하면 종료", edgeStatuses: [][]int{nil, nil, nil, nil},
			freshEdges: []int{2, 3}, wantRequests: []int{0, 1, 2, 3},
			wantManifests: 2, wantResult: state.ResultFailed,
		},
		{
			name: "모든 Edge 실패로 갱신한 뒤 403을 받아도 다시 갱신하지 않음", edgeStatuses: [][]int{nil, nil, {http.StatusForbidden}, {http.StatusOK}},
			freshEdges: []int{2, 3}, wantRequests: []int{0, 1, 2},
			wantManifests: 2, wantResult: state.ResultFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hosts := make(map[string]int)
			var urls []string
			var closedEdges []*httptest.Server
			for edge, statuses := range tt.edgeStatuses {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					status := pick(statuses, int(requests.Add(1)-1))
					if status == timeout {
						<-r.Context().Done()
						return
					}
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = fmt.Fprint(w, firmware)
					}
				}))
				t.Cleanup(server.Close)
				if len(statuses) == 0 {
					closedEdges = append(closedEdges, server)
				}
				urls = append(urls, server.URL)
				hosts[server.Listener.Addr().String()] = edge
			}
			for _, server := range closedEdges {
				server.Close()
			}

			transport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			var requests []int
			var paths []string
			client := &http.Client{
				Timeout: 500 * time.Millisecond,
				Transport: edgeRoundTripper(func(req *http.Request) (*http.Response, error) {
					requests = append(requests, hosts[req.URL.Host])
					paths = append(paths, req.URL.Path)
					return transport.RoundTrip(req)
				}),
			}
			manifest := otaclient.Manifest{
				CampaignID: 1, TargetVersion: "1.1.0",
				FileSize: int64(len(firmware)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(firmware))),
				DownloadURL:  urls[0] + "/old",
				URLExpiresAt: tt.initialExpiresAt,
				Edge:         &otaclient.ManifestEdge{EdgeID: "edge-0"},
			}
			if tt.hashMismatch {
				manifest.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("different-firmware")))
			}
			oldURLs := urls
			if len(tt.freshEdges) > 0 {
				oldURLs = urls[:2]
			}
			for i, url := range oldURLs[1:] {
				manifest.FallbackURLs = append(manifest.FallbackURLs, otaclient.FallbackURL{
					URL: url + "/old-fallback", EdgeID: fmt.Sprintf("edge-%d", i+1),
				})
			}
			fresh := manifest
			fresh.DownloadURL = urls[0] + "/new"
			fresh.URLExpiresAt = tt.freshExpiresAt
			if len(tt.freshEdges) > 0 {
				fresh.DownloadURL = urls[tt.freshEdges[0]] + "/new"
				fresh.Edge = &otaclient.ManifestEdge{EdgeID: fmt.Sprintf("edge-%d", tt.freshEdges[0])}
				fresh.FallbackURLs = nil
				for _, edge := range tt.freshEdges[1:] {
					fresh.FallbackURLs = append(fresh.FallbackURLs, otaclient.FallbackURL{
						URL: urls[edge] + "/new-fallback", EdgeID: fmt.Sprintf("edge-%d", edge),
					})
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ota := &fakeOTA{
				registerResponses: []otaclient.RegisterResponse{{VehicleToken: "tok-1"}},
				checkInResponses:  []otaclient.CheckInResponse{{UpdateTarget: true, CampaignID: 1}, {}},
				manifestResponses: []otaclient.Manifest{manifest, fresh},
				onCheckIn: func(call int) {
					if call == 1 {
						cancel()
					}
				},
			}
			store := &fakeStore{load: state.Snapshot{VehicleID: "v1", Status: state.StatusIdle, CurrentVersion: "1.0.0"}}
			inst := &fakeInstaller{errs: []error{tt.installErr}}
			m := machine.New(ota, downloader.New(t.TempDir(), client), inst, store, &fakeSignatureVerifier{}, nil, testIdentity, "enroll-key", fastCheckIn)

			require.ErrorIs(t, runMachine(t, m, ctx, "1.0.0"), context.Canceled)

			require.Equal(t, tt.wantRequests, requests)
			assert.Len(t, ota.manifestCalls, tt.wantManifests)
			if tt.wantPaths != nil {
				assert.Equal(t, tt.wantPaths, paths)
			}
			require.Len(t, ota.checkInCalls, 2)
			update := ota.checkInCalls[1].LastUpdate
			require.NotNil(t, update)
			assert.Equal(t, string(tt.wantResult), update.Result)
			if tt.hashMismatch {
				assert.Equal(t, string(state.FailureHashMismatch), update.FailureReason)
				assert.Equal(t, "edge-1", update.EdgeID)
				assert.Empty(t, inst.calls)
				return
			}
			if tt.installErr != nil {
				assert.Equal(t, string(state.FailureInstallFailed), update.FailureReason)
			} else if tt.wantResult == state.ResultFailed {
				assert.Equal(t, string(state.FailureDownloadFailed), update.FailureReason)
				assert.Empty(t, update.EdgeID)
				assert.Empty(t, inst.calls)
				return
			}
			wantEdgeID := fmt.Sprintf("edge-%d", tt.wantRequests[len(tt.wantRequests)-1])
			assert.Equal(t, wantEdgeID, update.EdgeID)
			require.GreaterOrEqual(t, len(store.saves), 2)
			installing := store.saves[1]
			assert.Equal(t, state.StatusInstalling, installing.Status)
			require.NotNil(t, installing.PendingUpdate)
			assert.Equal(t, wantEdgeID, installing.PendingUpdate.EdgeID)
		})
	}
}

type edgeRoundTripper func(*http.Request) (*http.Response, error)

func (rt edgeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt(req)
}
