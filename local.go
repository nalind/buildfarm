package buildfarm

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/containers/buildah"
	"github.com/containers/buildah/pkg/parse"
	"github.com/containers/common/libimage"
	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/podman/v4/pkg/domain/infra"
	"github.com/containers/storage"
	"github.com/nalind/buildfarm/emulation"
	"github.com/spf13/pflag"
)

type podmanLocal struct {
	name         string
	flagSet      *pflag.FlagSet
	config       *config.Config
	storeOptions storage.StoreOptions
	os           string
	arch         string
	variant      string
}

type listLocal struct {
	listName     string
	flagSet      *pflag.FlagSet
	config       *config.Config
	storeOptions storage.StoreOptions
}

func NewPodmanLocalImageBuilder(ctx context.Context, flags *pflag.FlagSet, storeOptions *storage.StoreOptions) (ImageBuilder, error) {
	if flags == nil {
		flags = pflag.NewFlagSet("buildfarm", pflag.ExitOnError)
	}
	if storeOptions == nil {
		storeOptions = &storage.StoreOptions{}
	}
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	nativePlatform := strings.Split(parse.DefaultPlatform(), "/")
	platform := make([]string, 3)
	copy(platform, nativePlatform)
	local := podmanLocal{
		name:    "local",
		flagSet: flags,
		config:  custom,
		storeOptions: storage.StoreOptions{
			GraphRoot:          storeOptions.GraphRoot,
			RunRoot:            storeOptions.RunRoot,
			GraphDriverName:    storeOptions.GraphDriverName,
			GraphDriverOptions: append([]string{}, storeOptions.GraphDriverOptions...),
		},
		os:      platform[0],
		arch:    platform[1],
		variant: platform[2],
	}
	return &local, nil
}

func (l *podmanLocal) WithEngine(ctx context.Context, fn func(ctx context.Context, engine entities.ImageEngine) error) error {
	podmanConfig := entities.PodmanConfig{
		FlagSet:                  l.flagSet,
		EngineMode:               entities.ABIMode,
		ContainersConf:           &config.Config{},
		ContainersConfDefaultsRO: l.config,
		Runroot:                  l.storeOptions.RunRoot,
		StorageDriver:            l.storeOptions.GraphDriverName,
		StorageOpts:              l.storeOptions.GraphDriverOptions,
	}
	engine, err := infra.NewImageEngine(&podmanConfig)
	if err != nil {
		return fmt.Errorf("initializing local image engine: %w", err)
	}
	// defer engine.Shutdown(ctx) - we actually get the same runtime every time, and we get errors if we try to use it after shutting it down. TODO: complain about it, loudly.
	err = fn(ctx, engine)
	if err != nil {
		return err
	}
	return nil
}

func (l *podmanLocal) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	native := parse.DefaultPlatform()
	emulated := emulation.Registered()
	return &Info{NativePlatform: native, EmulatedPlatforms: emulated}, nil
}

func (l *podmanLocal) Status(ctx context.Context) error {
	return l.WithEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error { return nil })
}

func (r *podmanLocal) Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error) {
	var report *entities.BuildReport
	var buildReport BuildReport
	err := r.WithEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
		var err error
		theseOptions := options
		theseOptions.Platforms = []struct{ OS, Arch, Variant string }{{r.os, r.arch, r.variant}}
		report, err = engine.Build(ctx, containerFiles, theseOptions)
		if err != nil {
			return fmt.Errorf("building for %q/%q/%q locally: %w", r.os, r.arch, r.variant, err)
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

func (r *podmanLocal) PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error) {
	err = r.WithEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
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

func (r *podmanLocal) PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error) {
	destination := options.Destination

	var br *entities.BoolReport
	if destination == nil {
		err = r.WithEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
			var err error
			br, err = engine.Exists(ctx, options.ImageID)
			return err
		})
	} else {
		br, err = destination.Exists(ctx, options.ImageID)
	}
	if err != nil {
		return "", err
	}
	if br.Value {
		return istorage.Transport.Name() + ":" + options.ImageID, nil
	}

	tempFile, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	err = r.WithEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
		saveOptions := entities.ImageSaveOptions{
			Format: options.SaveFormat,
			Output: tempFile.Name(),
		}
		if err := engine.Save(ctx, options.ImageID, nil, saveOptions); err != nil {
			return fmt.Errorf("saving image %q: %w", options.ImageID, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	loadOptions := entities.ImageLoadOptions{
		Input: tempFile.Name(),
	}
	if destination == nil {
		err = r.WithEngine(ctx, func(ctx context.Context, engine entities.ImageEngine) error {
			_, err := engine.Load(ctx, loadOptions)
			return err
		})
	} else {
		_, err = destination.Load(ctx, loadOptions)
	}
	if err != nil {
		return "", err
	}

	return istorage.Transport.Name() + ":" + options.ImageID, nil
}

func NewPodmanLocalListBuilder(listName string, flags *pflag.FlagSet, storeOptions *storage.StoreOptions) (ListBuilder, error) {
	if storeOptions == nil {
		storeOptions = &storage.StoreOptions{}
	}
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	ll := &listLocal{
		listName: listName,
		flagSet:  flags,
		config:   custom,
		storeOptions: storage.StoreOptions{
			GraphRoot:          storeOptions.GraphRoot,
			RunRoot:            storeOptions.RunRoot,
			GraphDriverName:    storeOptions.GraphDriverName,
			GraphDriverOptions: append([]string{}, storeOptions.GraphDriverOptions...),
		},
	}
	return ll, nil
}

func (l *listLocal) Build(ctx context.Context, images map[BuildReport]ImageBuilder) error {
	podmanConfig := entities.PodmanConfig{
		FlagSet:                  l.flagSet,
		EngineMode:               entities.ABIMode,
		ContainersConf:           &config.Config{},
		ContainersConfDefaultsRO: l.config,
		Runroot:                  l.storeOptions.RunRoot,
		StorageDriver:            l.storeOptions.GraphDriverName,
		StorageOpts:              l.storeOptions.GraphDriverOptions,
	}
	localEngine, err := infra.NewImageEngine(&podmanConfig)
	if err != nil {
		return fmt.Errorf("initializing local image engine: %w", err)
	}
	defer localEngine.Shutdown(ctx)

	localRuntime, err := libimage.RuntimeFromStoreOptions(nil, &l.storeOptions)
	if err != nil {
		return fmt.Errorf("initializing local manifest list storage: %w", err)
	}
	defer localRuntime.Shutdown(false)

	// find/create the list
	list, err := localRuntime.LookupManifestList(l.listName)
	if err != nil {
		list, err = localRuntime.CreateManifestList(l.listName)
	}
	if err != nil {
		return fmt.Errorf("creating manifest list %q: %w", l.listName, err)
	}

	// pull the images into local storage
	refs := make(map[string]ImageBuilder)
	for image, engine := range images {
		pullOptions := PullToLocalOptions{
			ImageID:     image.ImageID,
			SaveFormat:  image.SaveFormat,
			Destination: localEngine,
		}
		ref, err := engine.PullToLocal(ctx, pullOptions)
		if err != nil {
			return fmt.Errorf("pulling image %q to local storage: %w", image, err)
		}
		refs[ref] = engine
	}

	// clear the list in case it already existed
	listContents, err := list.Inspect()
	if err != nil {
		return fmt.Errorf("inspecting list %q: %w", l.listName, err)
	}
	for _, instance := range listContents.Manifests {
		if err := list.RemoveInstance(instance.Digest); err != nil {
			return fmt.Errorf("removing instance %q from list %q: %w", instance.Digest, l.listName, err)
		}
	}

	// add the images to the list
	for ref := range refs {
		options := libimage.ManifestListAddOptions{}
		if _, err := list.Add(ctx, ref, &options); err != nil {
			return fmt.Errorf("adding image %q to list: %w", ref, err)
		}
	}

	return nil
}
