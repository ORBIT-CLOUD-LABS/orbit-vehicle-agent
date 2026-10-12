// Command agent는 차량 에이전트의 진입점이다. 설정을 읽고 상태 저장소, OTA
// 클라이언트, 다운로더, 설치기, 상태 머신을 조립한 뒤, 종료 신호를 받을 때까지
// 등록·체크인·업데이트 루프를 실행한다.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/config"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/downloader"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/installer"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/machine"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/otaclient"
	"github.com/ORBIT-CLOUD-LABS/orbit-vehicle-agent/internal/state"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	if err := run(context.Background(), logger); err != nil {
		logger.Fatal("agent stopped", zap.Error(err))
	}
}

// run은 설정을 읽고 에이전트를 조립한 뒤, ctx가 취소될 때까지 상태 머신을
// 구동한다. ctx는 호출자나 SIGINT/SIGTERM으로 취소되며, 이 취소는 진행 중인
// 모든 HTTP 요청과 체크인 루프에 전달된다. 정상 종료(ctx 취소)면 nil을
// 반환하고, 그 밖의 에러는 에이전트가 시작하지 못했다는 뜻이다 (flow.md ③:
// 등록 키가 틀리면 시작 실패로 끝난다).
func run(ctx context.Context, logger *zap.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	m := assemble(cfg, logger)

	logger.Info("agent starting",
		zap.String("vehicle_id", cfg.VehicleID),
		zap.String("initial_version", cfg.InitialVersion),
		zap.String("ota_server_url", cfg.OTAServerURL),
	)

	if err := m.Run(ctx, cfg.InitialVersion); err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info("shutdown signal received, agent stopped")
			return nil
		}
		return err
	}
	return nil
}

// assemble은 에이전트의 구성요소를 연결한다. 저장된 차량 상태, OTA 서버
// 클라이언트, 펌웨어 다운로더와 설치기, 그리고 이들을 구동하는 상태 머신이다.
func assemble(cfg config.Config, logger *zap.Logger) *machine.Machine {
	store := state.NewStore(cfg.DataDir, cfg.VehicleID)
	ota := otaclient.New(cfg.OTAServerURL, newOTAHTTPClient())
	dl := downloader.New(cfg.DataDir, newCDNHTTPClient())
	inst := installer.New(cfg.DataDir)

	identity := machine.VehicleIdentity{
		VehicleID: cfg.VehicleID,
		Model:     cfg.VehicleModel,
		HWVersion: cfg.HWVersion,
		Region:    cfg.Region,
	}

	return machine.New(ota, dl, inst, store, logger, identity, cfg.EnrollmentKey,
		machine.WithCheckInInterval(cfg.CheckInInterval))
}

const (
	// otaRequestTimeout은 OTA 서버 API 요청 하나에 허용하는 전체 시간이다.
	otaRequestTimeout = 30 * time.Second
	// cdnResponseHeaderTimeout은 CDN이 응답 헤더를 보내기까지 기다리는 시간이다.
	cdnResponseHeaderTimeout = 30 * time.Second
)

// newOTAHTTPClient는 OTA 서버 API용 HTTP 클라이언트를 만든다. 서버가 응답하지
// 않아도 체크인 루프가 멈추지 않도록 요청마다 전체 시간 제한을 둔다.
func newOTAHTTPClient() *http.Client {
	return &http.Client{Timeout: otaRequestTimeout}
}

// newCDNHTTPClient는 펌웨어 다운로드용 HTTP 클라이언트를 만든다. 본문은 크기에
// 따라 오래 걸릴 수 있어 전체 시간 제한은 두지 않고, 연결·TLS(기본 Transport)와
// 응답 헤더 대기 시간만 제한한다.
func newCDNHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = cdnResponseHeaderTimeout
	return &http.Client{Transport: transport}
}
