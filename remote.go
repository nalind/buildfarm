package buildfarm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/containers/buildah"
	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/system"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/podman/v4/pkg/domain/infra"
	"github.com/spf13/pflag"
)

type podmanRemote struct {
	name      string
	flagSet   *pflag.FlagSet
	config    *config.Config
	uri       string
	identity  string
	isMachine bool
	os        string
	arch      string
	variant   string
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
	remote := podmanRemote{
		name:      name,
		flagSet:   flags,
		config:    custom,
		uri:       dest.URI,
		identity:  dest.Identity,
		isMachine: dest.IsMachine,
	}
	return &remote, nil
}

func (r *podmanRemote) withEngine(ctx context.Context, fn func(ctx context.Context, engine entities.ImageEngine) error) error {
	var connctx context.Context
	var err error
	if r.identity != "" {
		connctx, err = bindings.NewConnectionWithIdentity(ctx, r.uri, r.identity, r.isMachine)
		if err != nil {
			return fmt.Errorf("connecting to %q at %q as %q: %w", r.name, r.uri, r.identity, err)
		}
	} else {
		connctx, err = bindings.NewConnection(ctx, r.uri)
		if err != nil {
			return fmt.Errorf("connecting to %q at %q: %w", r.name, r.uri, err)
		}
	}
	remoteConfig := entities.PodmanConfig{
		FlagSet:    r.flagSet,
		EngineMode: entities.TunnelMode,
		URI:        r.uri,
		Identity:   r.identity,
	}
	engine, err := infra.NewImageEngine(&remoteConfig)
	if err != nil {
		return fmt.Errorf("initializing image engine at %q: %w", r.uri, err)
	}
	err = fn(connctx, engine)
	engine.Shutdown(connctx)
	return err
}

func (r *podmanRemote) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	var nativePlatform string
	err := r.withEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
		info, err := system.Info(ctx, &system.InfoOptions{})
		if err != nil {
			return fmt.Errorf("retrieving host info from %q: %w", r.name, err)
		}
		nativePlatform = info.Host.OS + "/" + info.Host.Arch // TODO: pester someone about returning variant info
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Info{NativePlatform: nativePlatform}, err
}

func (r *podmanRemote) Status(ctx context.Context) error {
	return r.withEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error { return nil })
}

func (r *podmanRemote) Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error) {
	var report *entities.BuildReport
	var buildReport BuildReport
	err := r.withEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
		var err error
		theseOptions := options
		theseOptions.Platforms = []struct{ OS, Arch, Variant string }{{r.os, r.arch, r.variant}}
		report, err = engine.Build(ctx, containerFiles, theseOptions)
		if err != nil {
			return fmt.Errorf("building for %q/%q/%q on %q: %w", r.os, r.arch, r.variant, r.name, err)
		}
		return nil
	})
	if err != nil {
		return BuildReport{}, err
	}
	buildReport.ImageID = report.ID
	buildReport.SaveFormat = "oci-archive"
	if options.OutputFormat == buildah.Dockerv2ImageManifest {
		buildReport.SaveFormat = "docker-archive"
	}
	return buildReport, nil
}

func (r *podmanRemote) PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error) {
	err = r.withEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
		saveOptions := entities.ImageSaveOptions{
			Format: options.SaveFormat,
			Output: options.SaveFile,
		}
		if err := engine.Save(ctx, options.ImageID, nil, saveOptions); err != nil {
			return fmt.Errorf("saving image %q: %w", options.ImageID, err)
		}
		return nil
	})
	return options.SaveFormat + ":" + options.SaveFile, nil
}

func (r *podmanRemote) PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error) {
	tempFile, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()
	err = r.withEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
		saveOptions := entities.ImageSaveOptions{
			Format: options.SaveFormat,
			Output: tempFile.Name(),
		}
		if err := engine.Save(ctx, options.ImageID, nil, saveOptions); err != nil {
			return fmt.Errorf("saving image %q to temporary file: %w", options.ImageID, err)
		}
		return nil
	})
	if err != nil {
		return "", err
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
