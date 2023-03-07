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
	nativePlatforms   []string
	emulatedPlatforms []string
}

// NewPodmanRemoteImageBuilder creates an ImageBuilder which uses a remote
// connection to a podman service running somewhere else.
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

// Name returns the remote engine's name.
func (r *podmanRemote) Name(ctx context.Context) string {
	return r.name
}

// Driver returns a description of implementation of this ImageBuilder.
func (r *podmanRemote) Driver(ctx context.Context) string {
	return "podman-remote"
}

// Done shuts down our connection to the remote engine.
func (r *podmanRemote) Done(ctx context.Context) error {
	if r.connCtx != nil && r.engine != nil {
		r.engine.Shutdown(r.connCtx)
		r.connCtx = nil
		r.engine = nil
	}
	return nil
}

func (r *podmanRemote) fetchInfo(ctx context.Context, options InfoOptions) (os, arch string, nativePlatforms []string, err error) {
	engineInfo, err := system.Info(r.connCtx, &system.InfoOptions{})
	if err != nil {
		return "", "", nil, fmt.Errorf("retrieving host info from %q: %w", r.name, err)
	}
	os = engineInfo.Host.OS
	arch = engineInfo.Host.Arch
	nativePlatform := os + "/" + arch // TODO: pester someone about returning variant info
	return os, arch, []string{nativePlatform}, nil
}

// Info returns information about the remote engine.
func (r *podmanRemote) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatforms, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return &Info{NativePlatforms: append([]string{}, r.nativePlatforms...)}, r.platformsErr
}

// NativePlatform returns the platforms that the remote engine can build for
// without requiring user space emulation.
func (r *podmanRemote) NativePlatforms(ctx context.Context, options InfoOptions) ([]string, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatforms, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return append([]string{}, r.nativePlatforms...), r.platformsErr
}

// EmulatedPlatforms returns the platforms that the remote engine can build for
// with the help of user space emulation.
func (r *podmanRemote) EmulatedPlatforms(ctx context.Context, options InfoOptions) ([]string, error) {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatforms, r.platformsErr = r.fetchInfo(ctx, options)
	})
	return nil, r.platformsErr
}

// Status returns the status of the connection to the remote engine.
func (r *podmanRemote) Status(ctx context.Context) error {
	r.platforms.Do(func() {
		r.os, r.arch, r.nativePlatforms, r.platformsErr = r.fetchInfo(ctx, InfoOptions{})
	})
	_, err := r.engine.Config(ctx)
	return err
}

// Build attempts a build using the specified build options.  If the build
// succeeds, it returns the built image's ID.
func (r *podmanRemote) Build(ctx context.Context, outputReference string, containerFiles []string, options BuildOptions) (BuildReport, error) {
	var buildReport BuildReport
	theseOptions := podmanBuildOptionsFromBuildOptions(options, r.os, r.arch, r.variant)
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

// PullToFile pulls the image from the remote engine and saves it to a file,
// returning a string-format reference which can be parsed by containers/image.
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

// PullToFile pulls the image from the remote engine and saves it to the local
// engine passed in via options, returning a string-format reference which can
// be parsed by containers/image.
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

// RemoveImage removes an image from the remote engine.
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

// PruneImages removes unused images from the remote engine.
func (r *podmanRemote) PruneImages(ctx context.Context, options PruneImageOptions) (PruneImageReport, error) {
	pruneReports, err := r.engine.Prune(ctx, entities.ImagePruneOptions{
		All:    options.All,
		Filter: []string{fmt.Sprintf("dangling=%v", !options.All)},
	})
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
