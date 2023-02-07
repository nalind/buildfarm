package main

import (
	"context"
	"fmt"

	"github.com/containers/buildah/define"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/nalind/buildfarm"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type buildOptions struct {
	Output       string
	Dockerfiles  []string
	BuildOptions entities.BuildOptions
}

var (
	buildDescription = `Builds a container image using podman system connections, then bundles them into a manifest list.`
	buildCommand     = &cobra.Command{
		Use:     "build",
		Short:   "Build a container image for multiple architectures",
		Long:    buildDescription,
		RunE:    buildCmd,
		Example: "  build [flags] buildContextDirectory",
		Args:    cobra.ExactArgs(1),
	}
	buildOpts = buildOptions{
		Output: "localhost/test",
		BuildOptions: entities.BuildOptions{
			BuildOptions: define.BuildOptions{
				ContextDirectory: ".",
				Layers:           true,
			},
		},
	}
)

func buildCmd(cmd *cobra.Command, args []string) error {
	ctx := context.TODO()
	if buildOpts.Output == "" {
		return fmt.Errorf(`no output specified: expected -t "dir:/directory/path" or -t "registry.tld/repository/name:tag"`)
	}
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

	if err = farm.Build(ctx, buildOpts.Output, schedule, buildOpts.Dockerfiles, buildOpts.BuildOptions); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	logrus.Infof("build: ok")
	return nil
}

func init() {
	mainCmd.AddCommand(buildCommand)
	buildCommand.PersistentFlags().StringVarP(&buildOpts.Output, "tag", "t", "", "output location")
	buildCommand.PersistentFlags().StringSliceVarP(&buildOpts.Dockerfiles, "file", "f", nil, "dockerfile")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.BuildOptions.Labels, "label", nil, "set `label=value` in output images")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.BuildOptions.RemoveIntermediateCtrs, "rm", false, "remove intermediate images on success")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.BuildOptions.ForceRmIntermediateCtrs, "force-rm", false, "remove intermediate images, even if the build fails")

}
