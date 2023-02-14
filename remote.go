package buildfarm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"sync"

	"github.com/containers/buildah"
	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/system"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/podman/v4/pkg/domain/infra"
	"github.com/hashicorp/go-multierror"
	"github.com/spf13/pflag"
)

type podmanRemote struct {
	name              string
	flagSet           *pflag.FlagSet
	config            *config.Config
	uri               string
	identity          string
	isMachine         bool
	connCtx           context.Context
	engine            entities.ImageEngine
	platforms         sync.Once
	platformsErr      error
	os                string
	arch              string
	variant           string
	nativePlatform    string
	emulatedPlatforms []string
}

func NewPodmanRemoteImageBuilder(ctx context.Context, flags *pflag.FlagSet, name string) (ImageBuilder, error) {
	if flags == nil {
		flags = pflag.NewFlagSet("buildfarm", pflag.ExitOnError)
	}
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	dest, ok := custom.Engine.ServiceDestinations[name]
	if !ok {
		err = fmt.Errorf("attempted to use destination %q, but no such destination exists", name)
	}
	var connCtx context.Context
	if dest.Identity != "" {
		connCtx, err = bindings.NewConnectionWithIdentity(ctx, dest.URI, dest.Identity, dest.IsMachine)
		if err != nil {
			return nil, fmt.Errorf("connecting to %q at %q as %q: %w", name, dest.URI, dest.Identity, err)
		}
	} else {
		connCtx, err = bindings.NewConnection(ctx, dest.URI)
		if err != nil {
			return nil, fmt.Errorf("connecting to %q at %q: %w", name, dest.URI, err)
		}
	}
	remoteConfig := entities.PodmanConfig{
		FlagSet:    flags,
		EngineMode: entities.TunnelMode,
		URI:        dest.URI,
		Identity:   dest.Identity,
	}
	engine, err := infra.NewImageEngine(&remoteConfig)
	if err != nil {
		return nil, fmt.Errorf("initializing image engine at %q: %w", dest.URI, err)
	}
	remote := podmanRemote{
		name:      name,
		flagSet:   flags,
		config:    custom,
		uri:       dest.URI,
		identity:  dest.Identity,
		isMachine: dest.IsMachine,
		connCtx:   connCtx,
		engine:    engine,
	}
	return &remote, nil
}

func (r *podmanRemote) Name(ctx context.Context) string {
	return r.name
}

func (r *podmanRemote) Driver(ctx context.Context) string {
	return "podman-remote"
}

func (r *podmanRemote) Done(ctx context.Context) error {
	if r.connCtx != nil && r.engine != nil {
		r.engine.Shutdown(r.connCtx)
		r.connCtx = nil
		r.engine = nil
	}
	return nil
}

func (r *podmanRemote) fetchInfo(ctx context.Context, options InfoOptions) (os, arch, nativePlatform string, err error) {
	engineInfo, err := system.Info(r.connCtx, &system.InfoOptions{})
	if err != nil {
		return "", "", "", fmt.Errorf("retrieving host info from %q: %w", r.name, err)
	}
	os = engineInfo.Host.OS
	arch = engineInfo.Host.Arch
	nativePlatform = os + "/" + arch // TODO: pester someone about returning variant info
	return os, arch, nativePlatform, nil
}

func (r *podmanRemote) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return &Info{NativePlatform: r.nativePlatform}, r.platformsErr
}

func (r *podmanRemote) NativePlatform(ctx context.Context, options InfoOptions) (string, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return r.nativePlatform, r.platformsErr
}

func (r *podmanRemote) EmulatedPlatforms(ctx context.Context, options InfoOptions) ([]string, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return nil, r.platformsErr
}

func (r *podmanRemote) Status(ctx context.Context) error {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatform, r.platformsErr = r.fetchInfo(ctx, InfoOptions{})
	})
	return r.platformsErr
}

func (r *podmanRemote) Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error) {
	var buildReport BuildReport
	theseOptions := options
	theseOptions.Platforms = []struct{ OS, Arch, Variant string }{{r.os, r.arch, r.variant}}
	report, err := r.engine.Build(ctx, containerFiles, theseOptions)
	if err != nil {
		return buildReport, fmt.Errorf("building for %v on %q: %w", theseOptions.Platforms, r.name, err)
	}
	buildReport.ImageID = report.ID
	buildReport.SaveFormat = "oci-archive"
	if options.OutputFormat == buildah.Dockerv2ImageManifest {
		buildReport.SaveFormat = "docker-archive"
	}
	return buildReport, nil
}

func (r *podmanRemote) PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error) {
	saveOptions := entities.ImageSaveOptions{
		Format: options.SaveFormat,
		Output: options.SaveFile,
	}
	if err := r.engine.Save(ctx, options.ImageID, nil, saveOptions); err != nil {
		return "", fmt.Errorf("saving image %q: %w", options.ImageID, err)
	}
	return options.SaveFormat + ":" + options.SaveFile, nil
}

func (r *podmanRemote) PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error) {
	tempFile, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()
	saveOptions := entities.ImageSaveOptions{
		Format: options.SaveFormat,
		Output: tempFile.Name(),
	}
	if err := r.engine.Save(ctx, options.ImageID, nil, saveOptions); err != nil {
		return "", fmt.Errorf("saving image %q to temporary file: %w", options.ImageID, err)
	}
	loadOptions := entities.ImageLoadOptions{
		Input: tempFile.Name(),
	}
	if options.Destination == nil {
		return "", errors.New("internal error: options.Destination not set")
	} else {
		if _, err = options.Destination.Load(ctx, loadOptions); err != nil {
			return "", fmt.Errorf("loading image %q: %w", options.ImageID, err)
		}
	}
	name := fmt.Sprintf("%s:%s", istorage.Transport.Name(), options.ImageID)
	return name, err
}

func (r *podmanRemote) RemoveImage(ctx context.Context, options RemoveImageOptions) error {
	rmOptions := entities.ImageRemoveOptions{}
	report, errs := r.engine.Remove(ctx, []string{options.ImageID}, rmOptions)
	if len(errs) > 0 {
		if len(errs) > 1 {
			var err *multierror.Error
			for _, e := range errs {
				err = multierror.Append(err, e)
			}
			if multi := err.ErrorOrNil(); multi != nil {
				return fmt.Errorf("removing intermediate image %q from remote %q: %w", options.ImageID, r.name, multi)
			}
			return nil
		} else {
			return fmt.Errorf("removing intermediate image %q from local storage: %w", options.ImageID, errs[0])
		}
	}
	if report.ExitCode != 0 {
		return fmt.Errorf("removing intermediate image %q from remote %q: status %d", options.ImageID, r.name, report.ExitCode)
	}
	return nil
}

func (r *podmanRemote) PruneImages(ctx context.Context, options PruneImageOptions) (PruneImageReport, error) {
	pruneReports, err := r.engine.Prune(ctx, entities.ImagePruneOptions{All: true, Filter: []string{"dangling=false"}})
	if err != nil {
		return PruneImageReport{}, fmt.Errorf("removing unused images from local storage: %w", err)
	}
	var report PruneImageReport
	for _, pruneReport := range pruneReports {
		if pruneReport.Err == nil && pruneReport.Id != "" {
			report.ImageIDs = append(report.ImageIDs, pruneReport.Id)
		}
	}
	return report, nil
}
