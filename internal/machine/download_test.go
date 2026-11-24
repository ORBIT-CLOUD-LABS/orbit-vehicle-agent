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
	tests := []struct {
		name            string
		primaryStatuses []int
		secondaryDown   bool
		wantRequests    []int
		wantManifests   int
		wantResult      state.Result
	}{
		{
			name: "502 후 같은 Edge에서 성공", primaryStatuses: []int{http.StatusBadGateway, http.StatusOK},
			wantRequests: []int{0, 0}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "504 재시도 초과 후 목록 순서대로 전환", primaryStatuses: []int{http.StatusGatewayTimeout}, secondaryDown: true,
			wantRequests: []int{0, 0, 0, 1, 2}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "연결 실패 후 즉시 전환", primaryStatuses: nil,
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "타임아웃 후 즉시 전환", primaryStatuses: []int{timeout},
			wantRequests: []int{0, 1}, wantManifests: 1, wantResult: state.ResultSucceeded,
		},
		{
			name: "404는 즉시 실패", primaryStatuses: []int{http.StatusNotFound},
			wantRequests: []int{0}, wantManifests: 1, wantResult: state.ResultFailed,
		},
		{
			name: "403은 같은 Edge에서 새 URL로 재시도", primaryStatuses: []int{http.StatusForbidden, http.StatusOK},
			wantRequests: []int{0, 0}, wantManifests: 2, wantResult: state.ResultSucceeded,
		},
		{
			name: "410이 반복되면 전환 없이 실패", primaryStatuses: []int{http.StatusGone},
			wantRequests: []int{0, 0}, wantManifests: 2, wantResult: state.ResultFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hosts := make(map[string]int)
			edgeStatuses := [][]int{tt.primaryStatuses, {http.StatusOK}, {http.StatusOK}}
			if tt.secondaryDown {
				edgeStatuses[1] = nil
			}
			var urls []string
			var closedEdges []*httptest.Server
			for edge, statuses := range edgeStatuses {
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
				FallbackURLs: []otaclient.FallbackURL{{URL: urls[1]}, {URL: urls[2]}},
			}
			fresh := manifest
			fresh.DownloadURL = urls[0] + "/new"
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
			m := machine.New(ota, downloader.New(t.TempDir(), client), &fakeInstaller{}, store, nil, testIdentity, "enroll-key", fastCheckIn)

			require.ErrorIs(t, runMachine(t, m, ctx, "1.0.0"), context.Canceled)

			require.Equal(t, tt.wantRequests, requests)
			assert.Len(t, ota.manifestCalls, tt.wantManifests)
			if tt.wantManifests == 2 {
				assert.Equal(t, []string{"/old", "/new"}, paths)
			}
			require.Len(t, ota.checkInCalls, 2)
			update := ota.checkInCalls[1].LastUpdate
			require.NotNil(t, update)
			assert.Equal(t, string(tt.wantResult), update.Result)
			if tt.wantResult == state.ResultFailed {
				assert.Equal(t, string(state.FailureDownloadFailed), update.FailureReason)
			}
		})
	}
}

type edgeRoundTripper func(*http.Request) (*http.Response, error)

func (rt edgeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt(req)
}
