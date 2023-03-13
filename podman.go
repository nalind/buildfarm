package buildfarm

import (
	"github.com/containers/buildah/define"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/podman/v4/pkg/domain/entities"
)

func podmanBuildOptionsFromBuildOptions(options BuildOptions, os, arch, variant string) entities.BuildOptions {
	pullPolicy := define.PullIfMissing
	if options.Pull {
		pullPolicy = define.PullAlways
	}
	configureNetwork := define.NetworkDefault
	if options.ConfigureNetwork != nil {
		if *options.ConfigureNetwork {
			configureNetwork = define.NetworkEnabled
		} else {
			configureNetwork = define.NetworkDisabled
		}
	}
	theseOptions := entities.BuildOptions{
		BuildOptions: define.BuildOptions{
			Layers: true,
			CommonBuildOpts: &define.CommonBuildOptions{
				ShmSize:      options.ShmSize,
				Ulimit:       append([]string{}, options.Ulimit...),
				Memory:       options.Memory,
				MemorySwap:   options.MemorySwap,
				CPUShares:    options.CPUShares,
				CPUQuota:     options.CPUQuota,
				CPUPeriod:    options.CPUPeriod,
				CPUSetCPUs:   options.CPUSetCPUs,
				CPUSetMems:   options.CPUSetMems,
				CgroupParent: options.CgroupParent,
				AddHost:      append([]string{}, options.AddHost...),
			},
			OutputFormat:            options.OutputFormat,
			Out:                     options.Out,
			Err:                     options.Err,
			ForceRmIntermediateCtrs: options.ForceRemoveIntermediateContainers,
			RemoveIntermediateCtrs:  options.RemoveIntermediateContainers,
			Platforms:               []struct{ OS, Arch, Variant string }{{os, arch, variant}},
			PullPolicy:              pullPolicy,
			IIDFile:                 options.IIDFile,
			ContextDirectory:        options.ContextDirectory,
			Labels:                  append([]string{}, options.Labels...),
			Args:                    copyStringStringMap(options.Args),
			NoCache:                 options.NoCache,
			Quiet:                   options.Quiet,
			CacheFrom:               append([]reference.Named{}, options.CacheFrom...),
			CacheTo:                 append([]reference.Named{}, options.CacheTo...),
			Target:                  options.Target,
			ConfigureNetwork:        configureNetwork,
		},
	}
	return theseOptions
}
