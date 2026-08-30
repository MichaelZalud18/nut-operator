package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	talosclient "github.com/siderolabs/talos/pkg/machinery/client"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

type talosShutdownRequest struct {
	ConfigPath string
	Endpoints  []string
	Node       string
	Force      bool
}

var executeTalosShutdown = realExecuteTalosShutdown

func verifyTalosShutdownAvailable(config actuatorConfig) error {
	if config.TalosConfigPath == "" {
		return fmt.Errorf("POWER_TALOS_CONFIG is required")
	}
	if config.TalosNode == "" {
		return fmt.Errorf("POWER_TALOS_NODE is required")
	}
	if len(config.TalosEndpoints) == 0 {
		return fmt.Errorf("POWER_TALOS_ENDPOINTS requires at least one endpoint")
	}
	file, err := os.Open(config.TalosConfigPath)
	if err != nil {
		return fmt.Errorf("open talosconfig: %w", err)
	}
	return file.Close()
}

func runTalosShutdown(logger *log.Logger, config actuatorConfig, payload nodeagent.ShutdownSignal) error {
	trace := newGateTrace(logger, payload.NodeName, payload.ExecutionID)
	if err := verifyTalosShutdownAvailable(config); err != nil {
		trace.fail(gateTalosCredential, err.Error())
		return err
	}
	trace.pass(gateTalosCredential, "talosconfig is readable")
	trace.pass(gateTalosTarget, "target "+config.TalosNode+" through "+joinSorted(config.TalosEndpoints))

	timeout := config.TalosShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	trace.pass(gateTalosAPICall, "Talos MachineService.Shutdown force=true")
	if err := executeTalosShutdown(ctx, talosShutdownRequest{
		ConfigPath: config.TalosConfigPath,
		Endpoints:  append([]string(nil), config.TalosEndpoints...),
		Node:       config.TalosNode,
		Force:      true,
	}); err != nil {
		trace.fail(gateTalosAPICall, err.Error())
		return err
	}
	return nil
}

func realExecuteTalosShutdown(ctx context.Context, request talosShutdownRequest) error {
	client, err := talosclient.New(ctx,
		talosclient.WithConfigFromFile(request.ConfigPath),
		talosclient.WithEndpoints(request.Endpoints...),
	)
	if err != nil {
		return fmt.Errorf("create Talos client: %w", err)
	}
	defer func() {
		_ = client.Close()
	}()

	ctx = talosclient.WithNodes(ctx, request.Node)
	options := []talosclient.ShutdownOption{}
	if request.Force {
		options = append(options, talosclient.WithShutdownForce(true))
	}
	if err := client.Shutdown(ctx, options...); err != nil {
		return fmt.Errorf("call Talos shutdown: %w", err)
	}
	return nil
}
