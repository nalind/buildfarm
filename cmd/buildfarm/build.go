package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/containers/buildah/define"
	"github.com/containers/buildah/pkg/util"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type buildOptions struct {
	output       string
	dockerfiles  []string
	buildOptions entities.BuildOptions
	platforms    []string
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
		output: "localhost/test",
		buildOptions: entities.BuildOptions{
			BuildOptions: define.BuildOptions{
				Layers:          true,
				CommonBuildOpts: &define.CommonBuildOptions{},
			},
		},
	}
)

func buildCmd(cmd *cobra.Command, args []string) error {
	ctx := context.TODO()
	if buildOpts.output == "" {
		return fmt.Errorf(`no output specified: expected -t "dir:/directory/path" or -t "registry.tld/repository/name:tag"`)
	}
	buildOpts.buildOptions.ContextDirectory = args[0]
	if buildOpts.buildOptions.ContextDirectory == "" {
		return fmt.Errorf("expected location of build context")
	}
	if !filepath.IsAbs(buildOpts.buildOptions.ContextDirectory) {
		contextDir, err := filepath.Abs(buildOpts.buildOptions.ContextDirectory)
		if err != nil {
			return err
		}
		buildOpts.buildOptions.ContextDirectory = contextDir
	}
	for _, arg := range buildOpts.cli.BuildArg {
		if buildOpts.buildOptions.Args == nil {
			buildOpts.buildOptions.Args = make(map[string]string)
		}
		kv := strings.SplitN(arg, "=", 2)
		if len(kv) > 1 {
			buildOpts.buildOptions.Args[kv[0]] = kv[1]
		} else {
			delete(buildOpts.buildOptions.Args, kv[0])
		}
	}
	for _, cacheFrom := range buildOpts.cli.CacheFrom {
		name, err := reference.ParseNamed(cacheFrom)
		if err != nil {
			return fmt.Errorf("parsing reference %q: %w", cacheFrom, err)
		}
		buildOpts.buildOptions.CacheFrom = append(buildOpts.buildOptions.CacheFrom, name)
	}
	switch buildOpts.cli.Network {
	case "", "default":
		// nothing
	case "none":
		buildOpts.buildOptions.ConfigureNetwork = define.NetworkDisabled
	}
	switch buildOpts.cli.Pull {
	case define.PullIfMissing.String():
		buildOpts.buildOptions.PullPolicy = define.PullIfMissing
	case define.PullIfNewer.String():
		buildOpts.buildOptions.PullPolicy = define.PullIfNewer
	case "true", "always", define.PullAlways.String():
		buildOpts.buildOptions.PullPolicy = define.PullAlways
	case "false", "never", define.PullNever.String():
		buildOpts.buildOptions.PullPolicy = define.PullNever
	case "", "default", "if-missing":
	default:
	}
	if len(buildOpts.dockerfiles) == 0 {
		dockerfile, err := util.DiscoverContainerfile(buildOpts.buildOptions.ContextDirectory)
		if err != nil {
			return err
		}
		absDockerfile := dockerfile
		if !filepath.IsAbs(dockerfile) {
			absDockerfile, err = filepath.Abs(dockerfile)
			if err != nil {
				return err
			}
		}
		buildOpts.dockerfiles = append(buildOpts.dockerfiles, absDockerfile)
	}

	farm, err := getFarm(ctx)
	if err != nil {
		return fmt.Errorf("initializing: %w", err)
	}
	globalFarm = farm

	schedule, err := farm.Schedule(ctx, buildOpts.platforms)
	if err != nil {
		return fmt.Errorf("scheduling builds: %w", err)
	}
	logrus.Debugf("schedule: %v", schedule)

	if err = farm.Build(ctx, buildOpts.output, schedule, buildOpts.dockerfiles, buildOpts.buildOptions); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	logrus.Infof("build: ok")
	return nil
}

func init() {
	mainCmd.AddCommand(buildCommand)
	// We intentionally avoid reusing parsing logic in buildah to not
	// advertise options which aren't supported for remote builds.
	buildCommand.PersistentFlags().StringVarP(&buildOpts.output, "tag", "t", "", "output location")
	buildCommand.PersistentFlags().StringSliceVarP(&buildOpts.dockerfiles, "file", "f", nil, "dockerfile")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.buildOptions.Labels, "label", nil, "set `label=value` in output images")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.buildOptions.RemoveIntermediateCtrs, "rm", true, "remove intermediate images on success")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.buildOptions.ForceRmIntermediateCtrs, "force-rm", false, "remove intermediate images, even if the build fails")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.buildOptions.CommonBuildOpts.AddHost, "add-host", nil, "add custom host-to-IP mappings")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.cli.BuildArg, "build-arg", nil, "build-time variable")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.cli.CacheFrom, "cache-from", nil, "cache source repositories")
	buildCommand.PersistentFlags().Uint64Var(&buildOpts.buildOptions.CommonBuildOpts.CPUPeriod, "cpu-period", 0, "CPU CFS period")
	buildCommand.PersistentFlags().Int64Var(&buildOpts.buildOptions.CommonBuildOpts.CPUQuota, "cpu-quota", 0, "CPU CFS quota")
	buildCommand.PersistentFlags().Uint64VarP(&buildOpts.buildOptions.CommonBuildOpts.CPUShares, "cpu-shares", "c", 0, "CPU CFS quota")
	buildCommand.PersistentFlags().StringVar(&buildOpts.buildOptions.CommonBuildOpts.CPUSetCPUs, "cpuset-cpus", "", "CPUs to allow execution")
	buildCommand.PersistentFlags().StringVar(&buildOpts.buildOptions.CommonBuildOpts.CPUSetMems, "cpuset-mems", "", "MEMs to allow execution")
	buildCommand.PersistentFlags().StringVar(&buildOpts.buildOptions.IIDFile, "iidfile", "", "write manifest list ID to file")
	buildCommand.PersistentFlags().StringVar(&buildOpts.cli.Isolation, "isolation", "", "RUN isolation")
	buildCommand.PersistentFlags().Int64Var(&buildOpts.buildOptions.CommonBuildOpts.Memory, "memory", 0, "memory limit")
	buildCommand.PersistentFlags().Int64Var(&buildOpts.buildOptions.CommonBuildOpts.MemorySwap, "memory-swap", 0, "memory+swap limit")
	buildCommand.PersistentFlags().StringVar(&buildOpts.cli.Network, "network", "", "RUN network")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.buildOptions.NoCache, "no-cache", false, "disable build cache")
	buildCommand.PersistentFlags().StringVar(&buildOpts.cli.Pull, "pull", define.PullIfMissing.String(), "always try to pull newer versions of images")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.platforms, "platform", nil, "target platforms")
	buildCommand.PersistentFlags().BoolVar(&buildOpts.buildOptions.Quiet, "quiet", false, "suppress build output")
	buildCommand.PersistentFlags().StringVar(&buildOpts.buildOptions.CommonBuildOpts.ShmSize, "shm-size", "", "size of /dev/shm")
	buildCommand.PersistentFlags().StringVar(&buildOpts.buildOptions.Target, "target", "", "final stage to build")
	buildCommand.PersistentFlags().StringSliceVar(&buildOpts.buildOptions.CommonBuildOpts.Ulimit, "ulimit", nil, "resource limits")
}
