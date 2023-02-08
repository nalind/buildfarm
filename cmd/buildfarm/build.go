package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/buildah/define"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/nalind/buildfarm"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type buildOptions struct {
	Output       string
	Dockerfiles  []string
	BuildOptions entities.BuildOptions
	cli          struct {
		BuildArg  []string
		CacheFrom []string
		Isolation string
		Network   string
		Pull      string
	}
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
				CommonBuildOpts:  &define.CommonBuildOptions{},
			},
		},
	}
)

func buildCmd(cmd *cobra.Command, args []string) error {
	ctx := context.TODO()
	if buildOpts.Output == "" {
		return fmt.Errorf(`no output specified: expected -t "dir:/directory/path" or -t "registry.tld/repository/name:tag"`)
	}
	for _, arg := range buildOpts.cli.BuildArg {
		if buildOpts.BuildOptions.Args == nil {
			buildOpts.BuildOptions.Args = make(map[string]string)
		}
		kv := strings.SplitN(arg, "=", 2)
		if len(kv) > 1 {
			buildOpts.BuildOptions.Args[kv[0]] = kv[1]
		} else {
			delete(buildOpts.BuildOptions.Args, kv[0])
		}
	}
	for _, cacheFrom := range buildOpts.cli.CacheFrom {
		name, err := reference.ParseNamed(cacheFrom)
		if err != nil {
			return fmt.Errorf("parsing reference %q: %w", cacheFrom, err)
		}
		buildOpts.BuildOptions.CacheFrom = append(buildOpts.BuildOptions.CacheFrom, name)
	}
	switch buildOpts.cli.Network {
	case "", "default":
		// nothing
	case "none":
		buildOpts.BuildOptions.ConfigureNetwork = define.NetworkDisabled
	}
	switch buildOpts.cli.Pull {
	case define.PullIfMissing.String():
		buildOpts.BuildOptions.PullPolicy = define.PullIfMissing
	case define.PullIfNewer.String():
		buildOpts.BuildOptions.PullPolicy = define.PullIfNewer
	case "true", "always", define.PullAlways.String():
		buildOpts.BuildOptions.PullPolicy = define.PullAlways
	case "false", "never", define.PullNever.String():
		buildOpts.BuildOptions.PullPolicy = define.PullNever
	case "", "default", "if-missing":
	default:
	}
	farm, err := buildfarm.GetFarm(ctx, "", globalStorageOptions, nil)
	if err != nil {
		return fmt.Errorf("initializing: %w", err)
	}

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
	buildCommand.PersistentFlags().BoolVar(&buildOpts.BuildOptions.RemoveIntermediateCtrs, "rm", true, "remove intermediate images on success")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.BuildOptions.ForceRmIntermediateCtrs, "force-rm", false, "remove intermediate images, even if the build fails")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.BuildOptions.CommonBuildOpts.AddHost, "add-host", nil, "add custom host-to-IP mappings")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.cli.BuildArg, "build-arg", nil, "build-time variable")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.cli.CacheFrom, "cache-from", nil, "cache source repositories")
	buildCommand.PersistentFlags().Uint64Var(&buildOpts.BuildOptions.CommonBuildOpts.CPUPeriod, "cpu-period", 0, "CPU CFS period")
	buildCommand.PersistentFlags().Int64Var(&buildOpts.BuildOptions.CommonBuildOpts.CPUQuota, "cpu-quota", 0, "CPU CFS quota")
	buildCommand.PersistentFlags().Uint64VarP(&buildOpts.BuildOptions.CommonBuildOpts.CPUShares, "cpu-shares", "c", 0, "CPU CFS quota")
	buildCommand.PersistentFlags().StringVar(&buildOpts.BuildOptions.CommonBuildOpts.CPUSetCPUs, "cpuset-cpus", "", "CPUs to allow execution")
	buildCommand.PersistentFlags().StringVar(&buildOpts.BuildOptions.CommonBuildOpts.CPUSetMems, "cpuset-mems", "", "MEMs to allow execution")
	buildCommand.PersistentFlags().StringVar(&buildOpts.BuildOptions.IIDFile, "iidfile", "", "write manifest list ID to file")
	buildCommand.PersistentFlags().StringVar(&buildOpts.cli.Isolation, "isolation", "", "RUN isolation")
	buildCommand.PersistentFlags().Int64Var(&buildOpts.BuildOptions.CommonBuildOpts.Memory, "memory", 0, "memory limit")
	buildCommand.PersistentFlags().Int64Var(&buildOpts.BuildOptions.CommonBuildOpts.MemorySwap, "memory-swap", 0, "memory+swap limit")
	buildCommand.PersistentFlags().StringVar(&buildOpts.cli.Network, "network", "", "RUN network")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.BuildOptions.NoCache, "no-cache", false, "disable build cache")
	buildCommand.PersistentFlags().StringVar(&buildOpts.cli.Pull, "pull", define.PullIfMissing.String(), "always try to pull newer versions of images")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.BuildOptions.Quiet, "quiet", false, "suppress build output")
	buildCommand.PersistentFlags().StringVar(&buildOpts.BuildOptions.CommonBuildOpts.ShmSize, "shm-size", "", "size of /dev/shm")
	buildCommand.PersistentFlags().StringVar(&buildOpts.BuildOptions.Target, "target", "", "final stage to build")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.BuildOptions.CommonBuildOpts.Ulimit, "ulimit", nil, "resource limits")
}
