package buildfarm

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"

	"github.com/containers/buildah"
	"github.com/containers/buildah/pkg/parse"
	"github.com/containers/common/libimage"
	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/podman/v4/pkg/domain/infra"
	"github.com/containers/storage"
	"github.com/hashicorp/go-multierror"
	"github.com/nalind/buildfarm/emulation"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
)

const (
	LocalImageBuilderName   = "(local)"
	localImageBuilderDriver = "local"
)

type podmanLocal struct {
	flagSet           *pflag.FlagSet
	config            *config.Config
	storeOptions      storage.StoreOptions
	engine            entities.ImageEngine
	platforms         sync.Once
	platformsErr      error
	os                string
	arch              string
	variant           string
	nativePlatforms   []string
	emulatedPlatforms []string
}

type listLocal struct {
	listName     string
	flagSet      *pflag.FlagSet
	config       *config.Config
	storeOptions storage.StoreOptions
	options      ListBuilderOptions
}

// NewPodmanLocalImageBuilder creates an ImageBuilder which uses libpod to directly perform a build.
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
	podmanConfig := entities.PodmanConfig{
		FlagSet:                  flags,
		EngineMode:               entities.ABIMode,
		ContainersConf:           &config.Config{},
		ContainersConfDefaultsRO: custom,
		Runroot:                  storeOptions.RunRoot,
		StorageDriver:            storeOptions.GraphDriverName,
		StorageOpts:              storeOptions.GraphDriverOptions,
	}
	engine, err := infra.NewImageEngine(&podmanConfig)
	if err != nil {
		return nil, fmt.Errorf("initializing local image engine: %w", err)
	}
	local := podmanLocal{
		flagSet: flags,
		config:  custom,
		storeOptions: storage.StoreOptions{
			GraphRoot:          storeOptions.GraphRoot,
			RunRoot:            storeOptions.RunRoot,
			GraphDriverName:    storeOptions.GraphDriverName,
			GraphDriverOptions: append([]string{}, storeOptions.GraphDriverOptions...),
		},
		engine: engine,
	}
	return &local, nil
}

// Name returns the local engine's name.
func (l *podmanLocal) Name(ctx context.Context) string {
	return LocalImageBuilderName
}

// Driver returns a description of implementation of this ImageBuilder.
func (l *podmanLocal) Driver(ctx context.Context) string {
	return localImageBuilderDriver
}

// Done would shut down our connection to the engine, but it can't be started
// back up, so it doesn't.
func (l *podmanLocal) Done(ctx context.Context) error {
	// return l.engine.Shutdown(ctx) - we actually get the same runtime every time, and we get errors if we try to use it after shutting it down. TODO: complain about it, loudly.
	return nil
}

func (l *podmanLocal) fetchInfo(ctx context.Context, options InfoOptions) (os, arch, variant string, nativePlatforms []string, emulatedPlatforms []string, err error) {
	nativePlatform := parse.DefaultPlatform()
	platform := strings.SplitN(nativePlatform, "/", 3)
	switch len(platform) {
	case 0, 1:
		return "", "", "", nil, nil, fmt.Errorf("unparseable default platform %q", nativePlatform)
	case 2:
		os, arch = platform[0], platform[1]
	case 3:
		os, arch, variant = platform[0], platform[1], platform[2]
	}
	os, arch, variant = libimage.NormalizePlatform(os, arch, variant)
	nativePlatform = os + "/" + arch
	if variant != "" {
		nativePlatform += ("/" + variant)
	}
	emulatedPlatforms = emulation.Registered()
	return os, arch, variant, append([]string{}, nativePlatform), emulatedPlatforms, nil
}

// Info returns information about the local engine.
func (l *podmanLocal) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	l.platforms.Do(func() {
		l.os, l.arch, l.variant, l.nativePlatforms, l.emulatedPlatforms, l.platformsErr = l.fetchInfo(ctx, options)
	})
	return &Info{NativePlatforms: append([]string{}, l.nativePlatforms...), EmulatedPlatforms: l.emulatedPlatforms}, l.platformsErr
}

// NativePlatform returns the platforms that the local engine can build for
// without requiring user space emulation.
func (l *podmanLocal) NativePlatforms(ctx context.Context, options InfoOptions) ([]string, error) {
	l.platforms.Do(func() {
		l.os, l.arch, l.variant, l.nativePlatforms, l.emulatedPlatforms, l.platformsErr = l.fetchInfo(ctx, options)
	})
	return append([]string{}, l.nativePlatforms...), l.platformsErr
}

// EmulatedPlatforms returns the platforms that the local engine can build for
// with the help of user space emulation.
func (l *podmanLocal) EmulatedPlatforms(ctx context.Context, options InfoOptions) ([]string, error) {
	l.platforms.Do(func() {
		l.os, l.arch, l.variant, l.nativePlatforms, l.emulatedPlatforms, l.platformsErr = l.fetchInfo(ctx, options)
	})
	return l.emulatedPlatforms, l.platformsErr
}

// Status returns the status of the connection to the local engine.
func (l *podmanLocal) Status(ctx context.Context) error {
	l.platforms.Do(func() {
		l.os, l.arch, l.variant, l.nativePlatforms, l.emulatedPlatforms, l.platformsErr = l.fetchInfo(ctx, InfoOptions{})
	})
	_, err := l.engine.Config(ctx)
	return err
}

// Build attempts a build using the specified build options.  If the build
// succeeds, it returns the built image's ID.
func (l *podmanLocal) Build(ctx context.Context, outputReference string, containerFiles []string, options BuildOptions) (BuildReport, error) {
	var buildReport BuildReport
	l.platforms.Do(func() {
		l.os, l.arch, l.variant, l.nativePlatforms, l.emulatedPlatforms, l.platformsErr = l.fetchInfo(ctx, InfoOptions{})
	})
	if l.platformsErr != nil {
		return buildReport, fmt.Errorf("determining local platform: %w", l.platformsErr)
	}
	theseOptions := podmanBuildOptionsFromBuildOptions(options, l.os, l.arch, l.variant)
	report, err := l.engine.Build(ctx, containerFiles, theseOptions)
	if err != nil {
		return buildReport, fmt.Errorf("building for %v locally: %w", theseOptions.Platforms, err)
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
func (r *podmanLocal) PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error) {
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
func (r *podmanLocal) PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error) {
	destination := options.Destination

	// already present at destination?
	var br *entities.BoolReport
	if destination == nil {
		br, err = r.engine.Exists(ctx, options.ImageID)
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

	saveOptions := entities.ImageSaveOptions{
		Format: options.SaveFormat,
		Output: tempFile.Name(),
	}
	if err := r.engine.Save(ctx, options.ImageID, nil, saveOptions); err != nil {
		return "", fmt.Errorf("saving image %q: %w", options.ImageID, err)
	}

	loadOptions := entities.ImageLoadOptions{
		Input: tempFile.Name(),
	}
	if destination == nil {
		_, err = r.engine.Load(ctx, loadOptions)
	} else {
		_, err = destination.Load(ctx, loadOptions)
	}
	if err != nil {
		return "", err
	}

	return istorage.Transport.Name() + ":" + options.ImageID, nil
}

// RemoveImage removes an image from the local engine.
func (r *podmanLocal) RemoveImage(ctx context.Context, options RemoveImageOptions) error {
	rmOptions := entities.ImageRemoveOptions{}
	report, errs := r.engine.Remove(ctx, []string{options.ImageID}, rmOptions)
	if len(errs) > 0 {
		if len(errs) > 1 {
			var err *multierror.Error
			for _, e := range errs {
				err = multierror.Append(err, e)
			}
			if multi := err.ErrorOrNil(); multi != nil {
				return fmt.Errorf("removing intermediate image %q from local storage: %w", options.ImageID, multi)
			}
			return nil
		} else {
			return fmt.Errorf("removing intermediate image %q from local storage: %w", options.ImageID, errs[0])
		}
	}
	if report.ExitCode != 0 {
		return fmt.Errorf("removing intermediate image %q from local storage: status %d", options.ImageID, report.ExitCode)
	}
	return nil
}

// PruneImages removes unused images from the local engine.
func (r *podmanLocal) PruneImages(ctx context.Context, options PruneImageOptions) (PruneImageReport, error) {
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

// NewPodmanLocalListBuilder returns a manifest list builder which saves a
// manifest list and images to local storage.
func NewPodmanLocalListBuilder(listName string, flags *pflag.FlagSet, storeOptions *storage.StoreOptions, options ListBuilderOptions) (ListBuilder, error) {
	if storeOptions == nil {
		storeOptions = &storage.StoreOptions{}
	}
	if options.IIDFile != "" {
		return nil, fmt.Errorf("local filesystem doesn't use image IDs, --iidfile not supported")
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
		options: options,
	}
	return ll, nil
}

// Build retrieves images from the build reports and assembles them into a
// manifest list in local container storage.
func (l *listLocal) Build(ctx context.Context, images map[BuildReport]ImageBuilder) (string, error) {
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
		return "", fmt.Errorf("initializing local image engine: %w", err)
	}
	defer localEngine.Shutdown(ctx)

	localRuntime, err := libimage.RuntimeFromStoreOptions(nil, &l.storeOptions)
	if err != nil {
		return "", fmt.Errorf("initializing local manifest list storage: %w", err)
	}
	defer localRuntime.Shutdown(false)

	// find/create the list
	list, err := localRuntime.LookupManifestList(l.listName)
	if err != nil {
		list, err = localRuntime.CreateManifestList(l.listName)
	}
	if err != nil {
		return "", fmt.Errorf("creating manifest list %q: %w", l.listName, err)
	}

	// pull the images into local storage
	var pullGroup multierror.Group
	refs := make(map[string]ImageBuilder)
	var refsMutex sync.Mutex
	for image, engine := range images {
		image, engine := image, engine
		pullOptions := PullToLocalOptions{
			ImageID:     image.ImageID,
			SaveFormat:  image.SaveFormat,
			Destination: localEngine,
		}
		pullGroup.Go(func() error {
			logrus.Infof("copying image %s", image.ImageID)
			defer logrus.Infof("copied image %s", image.ImageID)
			ref, err := engine.PullToLocal(ctx, pullOptions)
			if err != nil {
				return fmt.Errorf("pulling image %q to local storage: %w", image, err)
			}
			refsMutex.Lock()
			defer refsMutex.Unlock()
			refs[ref] = engine
			return nil
		})
	}
	pullErrors := pullGroup.Wait()
	err = pullErrors.ErrorOrNil()
	if err != nil {
		return "", fmt.Errorf("building: %w", err)
	}

	if l.options.RemoveIntermediateImages {
		var rmGroup multierror.Group
		for image, engine := range images {
			if engine.Name(ctx) == LocalImageBuilderName {
				continue
			}
			image, engine := image, engine
			rmGroup.Go(func() error {
				return engine.RemoveImage(ctx, RemoveImageOptions{ImageID: image.ImageID})
			})
		}
		rmErrors := rmGroup.Wait()
		if rmErrors != nil {
			if err = rmErrors.ErrorOrNil(); err != nil {
				return "", fmt.Errorf("removing intermediate images: %w", err)
			}
		}
	}

	// clear the list in case it already existed
	listContents, err := list.Inspect()
	if err != nil {
		return "", fmt.Errorf("inspecting list %q: %w", l.listName, err)
	}
	for _, instance := range listContents.Manifests {
		if err := list.RemoveInstance(instance.Digest); err != nil {
			return "", fmt.Errorf("removing instance %q from list %q: %w", instance.Digest, l.listName, err)
		}
	}

	// add the images to the list
	for ref := range refs {
		options := libimage.ManifestListAddOptions{}
		if _, err := list.Add(ctx, ref, &options); err != nil {
			return "", fmt.Errorf("adding image %q to list: %w", ref, err)
		}
	}

	return l.listName, nil
}
