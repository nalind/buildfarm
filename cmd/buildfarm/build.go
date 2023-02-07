package main

import (
	"context"
	"fmt"

	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/nalind/buildfarm"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	buildDescription = `Builds a container image using podman system connections, then bundles them into a manifest list.`
	buildCommand     = &cobra.Command{
		Use:     "build",
		Short:   "Build a container image for multiple architectures",
		Long:    buildDescription,
		RunE:    buildCmd,
		Example: "",
	}
)

func buildCmd(cmd *cobra.Command, args []string) error {
	var buildOptions entities.BuildOptions
	buildOptions.ContextDirectory = "."
	buildOptions.Layers = true

	ctx := context.TODO()
	farm, err := buildfarm.GetFarm(ctx, "", globalStorageOptions, nil)
	if err != nil {
		return fmt.Errorf("initializing: %w", err)
	}
	status, err := farm.Status(ctx)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	logrus.Debugf("status: %v", status)

	platforms, err := farm.NativePlatforms(ctx)
	if err != nil {
		return fmt.Errorf("platforms: %w", err)
	}
	logrus.Debugf("native platforms: %v", platforms)

	platforms, err = farm.EmulatedPlatforms(ctx)
	if err != nil {
		return fmt.Errorf("platforms: %w", err)
	}
	logrus.Debugf("emulated platforms: %v", platforms)

	schedule, err := farm.Schedule(ctx, nil)
	if err != nil {
		return fmt.Errorf("scheduling builds: %w", err)
	}
	logrus.Debugf("schedule: %v", schedule)

	if err = farm.Build(ctx, "localhost/test", schedule, nil, buildOptions); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	logrus.Infof("build: ok")
	return nil
}

func init() {
	mainCmd.AddCommand(buildCommand)
}
