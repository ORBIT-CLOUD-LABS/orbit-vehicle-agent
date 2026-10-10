// Command agent is the vehicle agent's entry point. It loads configuration,
// assembles the state store, OTA client, downloader, installer, and state
// machine, and runs the register/check-in/update loop until the process
// receives a termination signal.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

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

// run loads configuration, assembles the agent, and drives its state
// machine until ctx is cancelled — either by the caller or by SIGINT/SIGTERM,
// which this propagates to every in-flight HTTP request and to the
// check-in loop. A clean shutdown (ctx cancelled) returns nil; any other
// error means the agent could not start at all (flow.md ③: an invalid
// enrollment key ends startup).
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

// assemble wires the agent's components together: persisted vehicle state,
// the OTA server client, the firmware downloader and installer, and the
// state machine that drives them.
func assemble(cfg config.Config, logger *zap.Logger) *machine.Machine {
	store := state.NewStore(cfg.DataDir, cfg.VehicleID)
	ota := otaclient.New(cfg.OTAServerURL, http.DefaultClient)
	dl := downloader.New(cfg.DataDir, http.DefaultClient)
	inst := installer.New(cfg.DataDir)

	identity := machine.VehicleIdentity{
		VehicleID: cfg.VehicleID,
		Model:     cfg.VehicleModel,
		HWVersion: cfg.HWVersion,
		Region:    cfg.Region,
	}

	return machine.New(ota, dl, inst, store, logger, identity, cfg.EnrollmentKey)
}
