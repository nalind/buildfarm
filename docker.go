package buildfarm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/containers/buildah/define"
	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/storage/pkg/chrootarchive"
	"github.com/docker/go-units"
	docker "github.com/fsouza/go-dockerclient"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
)

type dockerEngine struct {
	name              string
	flagSet           *pflag.FlagSet
	config            *config.Config
	client            *docker.Client
	platforms         sync.Once
	platformsErr      error
	os                string
	arch              string
	variant           string
	nativePlatform    string
	emulatedPlatforms []string
}

func NewDockerImageBuilder(ctx context.Context, flags *pflag.FlagSet, name string) (ImageBuilder, error) {
	if flags == nil {
		flags = pflag.NewFlagSet("buildfarm", pflag.ExitOnError)
	}
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	endpoint, cert, key, ca := "", "", "", ""
	var client *docker.Client
	if cert != "" || key != "" || ca != "" {
		client, err = docker.NewTLSClient(endpoint, cert, key, ca)
	} else {
		client, err = docker.NewClient(endpoint)
	}
	if err != nil {
		return nil, err
	}
	remote := dockerEngine{
		name:    name,
		flagSet: flags,
		config:  custom,
		client:  client,
	}
	return &remote, nil
}

func (r *dockerEngine) Name(ctx context.Context) string {
	return r.name
}

func (r *dockerEngine) Driver(ctx context.Context) string {
	return "docker"
}

func (r *dockerEngine) Done(ctx context.Context) error {
	return nil
}

func (r *dockerEngine) fetchInfo(ctx context.Context, options InfoOptions) (os, arch, nativePlatform string, err error) {
	dockerInfo, infoErr := r.client.Info()
	if infoErr != nil {
		return "", "", "", fmt.Errorf("retrieving host info from %q: %w", r.name, infoErr)
	}
	os = dockerInfo.OperatingSystem
	arch = dockerInfo.Architecture
	nativePlatform = os + "/" + arch // TODO: pester someone about returning variant info
	return os, arch, nativePlatform, nil
}

func (r *dockerEngine) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return &Info{NativePlatform: r.nativePlatform}, r.platformsErr
}

func (r *dockerEngine) NativePlatform(ctx context.Context, options InfoOptions) (string, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return r.nativePlatform, r.platformsErr
}

func (r *dockerEngine) EmulatedPlatforms(ctx context.Context, options InfoOptions) ([]string, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return nil, r.platformsErr
}

func (r *dockerEngine) Status(ctx context.Context) error {
	return r.client.PingWithContext(ctx)
}

func (r *dockerEngine) Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error) {
	var report *entities.BuildReport
	var buildReport BuildReport
	dockerfile := ""
	if len(containerFiles) > 0 {
		dockerfile = containerFiles[0]
	}
	rc, err := chrootarchive.Tar(options.ContextDirectory, nil, options.ContextDirectory)
	if err != nil {
		return buildReport, fmt.Errorf("archiving %q: %w", options.ContextDirectory, err)
	}
	defer rc.Close()
	labelsMap := make(map[string]string)
	for _, label := range options.Labels {
		v := strings.SplitN(label, "=", 2)
		switch len(v) {
		case 2:
			labelsMap[v[0]] = v[1]
		case 1:
			labelsMap[v[0]] = ""
		default:
			return buildReport, fmt.Errorf("parsing requested label %q", label)
		}
	}
	shmSize, err := units.RAMInBytes(options.CommonBuildOpts.ShmSize)
	if err != nil {
		return buildReport, fmt.Errorf("parsing requested shmsize %q", options.CommonBuildOpts.ShmSize)
	}
	pull := false
	switch options.PullPolicy {
	case define.PullAlways, define.PullIfMissing, define.PullIfNewer:
		pull = true
	case define.PullNever:
		pull = false
	default:
		pull = true
	}
	var buildArgs []docker.BuildArg
	for arg, value := range options.Args {
		buildArgs = append(buildArgs, docker.BuildArg{Name: arg, Value: value})
	}
	var ulimits []docker.ULimit
	for _, ulimit := range options.CommonBuildOpts.Ulimit {
		u := strings.SplitN(ulimit, "=", 2)
		if len(u) != 2 {
			return buildReport, fmt.Errorf(`parsing ulimit %q: expected "limit=soft[:hard]"`, ulimit)
		}
		v := strings.SplitN(u[1], ":", 2)
		softString := v[0]
		hardString := v[0]
		if len(v) > 1 {
			hardString = v[1]
		}
		var hard, soft int64
		switch u[0] {
		case "core", "fsize", "memlock", "data", "rss", "stack":
			soft, err = units.RAMInBytes(softString)
			if err != nil {
				return buildReport, fmt.Errorf("parsing requested soft %q limit %q: %w", u[0], softString, err)
			}
			hard, err = units.RAMInBytes(hardString)
			if err != nil {
				return buildReport, fmt.Errorf("parsing requested hard %q limit %q: %w", u[0], hardString, err)
			}
		case "cpu", "locks", "msgqueue", "nice", "nofile", "nproc", "rtprio", "rttime", "sigpending":
			soft, err = strconv.ParseInt(softString, 10, 64)
			if err != nil {
				return buildReport, fmt.Errorf("parsing soft %q limit: %w", u[0], err)
			}
			hard, err = strconv.ParseInt(hardString, 10, 64)
			if err != nil {
				return buildReport, fmt.Errorf("parsing hard %q limit: %w", u[0], err)
			}
		}
		ulimits = append(ulimits, docker.ULimit{Name: u[0], Soft: soft, Hard: hard})
	}
	buildOptions := docker.BuildImageOptions{
		Name:                reference,
		InputStream:         rc,
		Platform:            r.os + "/" + r.arch,
		Context:             ctx,
		ContextDir:          options.ContextDirectory,
		Dockerfile:          dockerfile,
		RmTmpContainer:      options.RemoveIntermediateCtrs,
		ForceRmTmpContainer: options.ForceRmIntermediateCtrs,
		ExtraHosts:          "",
		CacheFrom:           nil,
		Labels:              labelsMap,
		Memory:              options.CommonBuildOpts.Memory,
		Memswap:             options.CommonBuildOpts.MemorySwap,
		ShmSize:             shmSize,
		CPUShares:           int64(options.CommonBuildOpts.CPUShares),
		CPUQuota:            options.CommonBuildOpts.CPUQuota,
		CPUPeriod:           int64(options.CommonBuildOpts.CPUPeriod),
		CPUSetCPUs:          options.CommonBuildOpts.CPUSetCPUs,
		CgroupParent:        options.CommonBuildOpts.CgroupParent,
		NoCache:             options.NoCache,
		SuppressOutput:      options.Quiet,
		Pull:                pull,
		BuildArgs:           buildArgs,
		Ulimits:             ulimits,
		/* TODO
		Auth                AuthConfiguration  `qs:"-"` // for older docker X-Registry-Auth header
		AuthConfigs         AuthConfigurations `qs:"-"` // for newer docker X-Registry-Config header
		NetworkMode         string             `ver:"1.25"`
		*/
	}
	if r.variant != "" {
		buildOptions.Platform += "/" + r.variant
	}
	if err := r.client.BuildImage(buildOptions); err != nil {
		return buildReport, fmt.Errorf("building for %v on %q: %w", buildOptions.Platform, r.name, err)
	}
	buildReport.ImageID = report.ID
	buildReport.SaveFormat = "docker-archive"
	return buildReport, nil
}

func (r *dockerEngine) PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error) {
	tempFile, err := os.Create(options.SaveFile)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			if e := os.Remove(tempFile.Name()); e != nil {
				logrus.Warnf("removing %q: %v", tempFile.Name(), e)
			}
		}
	}()
	defer tempFile.Close()
	exportOptions := docker.ExportImageOptions{
		Name:         options.ImageID,
		OutputStream: tempFile,
		Context:      ctx,
	}
	if err := r.client.ExportImage(exportOptions); err != nil {
		return "", fmt.Errorf("saving image %q: %w", options.ImageID, err)
	}
	return options.SaveFormat + ":" + options.SaveFile, nil
}

func (r *dockerEngine) PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error) {
	if options.Destination == nil {
		return "", errors.New("internal error: options.Destination not set")
	}
	tempFile, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()
	exportOptions := docker.ExportImageOptions{
		Name:         options.ImageID,
		OutputStream: tempFile,
		Context:      ctx,
	}
	if err := r.client.ExportImage(exportOptions); err != nil {
		return "", fmt.Errorf("saving image %q to temporary file: %w", options.ImageID, err)
	}
	loadOptions := entities.ImageLoadOptions{
		Input: tempFile.Name(),
	}
	if _, err = options.Destination.Load(ctx, loadOptions); err != nil {
		return "", fmt.Errorf("loading image %q: %w", options.ImageID, err)
	}
	name := fmt.Sprintf("%s:%s", istorage.Transport.Name(), options.ImageID)
	return name, err
}

func (r *dockerEngine) RemoveImage(ctx context.Context, options RemoveImageOptions) error {
	if err := r.client.RemoveImage(options.ImageID); err != nil {
		return fmt.Errorf("removing intermediate image %q from remote %q: %w", options.ImageID, r.name, err)
	}
	return nil
}

func (r *dockerEngine) PruneImages(ctx context.Context, options PruneImageOptions) (PruneImageReport, error) {
	pruneReports, err := r.client.PruneImages(docker.PruneImagesOptions{
		Filters: map[string][]string{"force": {"true"}, "dangling": {"false"}},
		Context: ctx,
	})
	if err != nil {
		return PruneImageReport{}, fmt.Errorf("removing unused images from remote %q: %w", r.name, err)
	}
	var report PruneImageReport
	for _, deleted := range pruneReports.ImagesDeleted {
		if deleted.Deleted != "" {
			report.ImageIDs = append(report.ImageIDs, deleted.Deleted)
		}
	}
	return report, nil
}
