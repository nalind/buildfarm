package buildfarm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/containers/buildah/define"
	"github.com/containers/common/libimage"
	"github.com/containers/common/pkg/config"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/storage"
	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
)

// Farm represents a group of connections to builders.
type Farm struct {
	Name         string
	FlagSet      *pflag.FlagSet
	storeOptions *storage.StoreOptions   // not nil -> use local engine, too
	Builders     map[string]ImageBuilder // name -> builder
}

// Schedule is a description of where and how we'll do builds.
type Schedule struct {
	PlatformBuilders map[string]string // target->connection
}

// CreateFarm creates an empty farm which will use a set of named connections
// which will be added to it later.
func CreateFarm(ctx context.Context, name string) (*Farm, error) {
	return nil, errors.New("not implemented")
}

// UpdateFarm updates a farm, adding and removing the set of named connections
// to/from it, respectively.
func UpdateFarm(ctx context.Context, add, remove []string) (*Farm, error) {
	return nil, errors.New("not implemented")
}

// NewDefaultFarm returns a Farm that uses all known system connections and
// which has no name.  If storeOptions is not nil, the local system will be
// included as an unnamed connection.
func NewDefaultFarm(ctx context.Context, storeOptions *storage.StoreOptions, flags *pflag.FlagSet) (*Farm, error) {
	logrus.Info("initializing default farm")
	defer logrus.Info("default farm ready")
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	farm := &Farm{
		Builders: make(map[string]ImageBuilder),
	}
	if flags == nil {
		flags = pflag.NewFlagSet("buildfarm", pflag.ExitOnError)
	}
	farm.FlagSet = flags
	farm.storeOptions = storeOptions
	var builderMutex sync.Mutex
	var builderGroup multierror.Group
	for dest := range custom.Engine.ServiceDestinations {
		dest := dest
		builderGroup.Go(func() error {
			logrus.Infof("connecting to %q", dest)
			defer logrus.Infof("builder %q ready", dest)
			ib, err := NewPodmanRemoteImageBuilder(ctx, flags, dest)
			if err != nil {
				return err
			}
			builderMutex.Lock()
			defer builderMutex.Unlock()
			farm.Builders[dest] = ib
			return nil
		})
	}
	if farm.storeOptions != nil { // make a shallow copy - could/should be a deep copy?
		builderGroup.Go(func() error {
			logrus.Infof("setting up local builder")
			defer logrus.Infof("local builder ready")
			ib, err := NewPodmanLocalImageBuilder(ctx, flags, farm.storeOptions)
			if err != nil {
				return err
			}
			builderMutex.Lock()
			defer builderMutex.Unlock()
			farm.Builders[""] = ib
			return nil
		})
	}
	if builderError := builderGroup.Wait(); builderError != nil {
		if err := builderError.ErrorOrNil(); err != nil {
			return nil, err
		}
	}
	if len(farm.Builders) > 0 {
		return farm, nil
	}
	return nil, errors.New("no builders configured")
}

// GetFarm returns a Farm that has a configured set of system connections.
func NewFarm(ctx context.Context, name string, storeOptions *storage.StoreOptions, flags *pflag.FlagSet) (*Farm, error) {
	if name == "" {
		return NewDefaultFarm(ctx, storeOptions, flags)
	}
	return nil, errors.New("not implemented")
}

// Prune, well, prunes unused images from each of the builders.  We remove
// images that we build after we've downloaded them when the Rm flag is true,
// which is its default, but that still leaves base images that we caused to be
// pulled lying around.
func (f *Farm) PruneImages(ctx context.Context, options PruneImageOptions) (map[string]PruneImageReport, error) {
	report := make(map[string]PruneImageReport)
	err := f.ForEach(ctx, func(ctx context.Context, name string, ib ImageBuilder) (bool, error) {
		pruneReport, err := ib.PruneImages(ctx, options)
		if err == nil {
			report[name] = PruneImageReport{
				ImageIDs: append([]string{}, pruneReport.ImageIDs...),
			}
		}
		return false, err
	})
	return report, err
}

// Done performs any necessary end-of-process cleanup for the farm's
// members.
func (f *Farm) Done(ctx context.Context) error {
	return f.ForEach(ctx, func(ctx context.Context, name string, ib ImageBuilder) (bool, error) {
		err := ib.Done(ctx)
		return false, err
	})
}

// Status polls the connections in the farm and returns a map of their
// individual status, along with an error if any are down or otherwise
// unreachable.
func (f *Farm) Status(ctx context.Context) (map[string]error, error) {
	status := make(map[string]error)
	var statusMutex sync.Mutex
	var statusGroup multierror.Group
	for _, builder := range f.Builders {
		builder := builder
		statusGroup.Go(func() error {
			logrus.Debugf("getting status of %q", builder.Name(ctx))
			defer logrus.Debugf("got status of %q", builder.Name(ctx))
			err := builder.Status(ctx)
			statusMutex.Lock()
			defer statusMutex.Unlock()
			status[builder.Name(ctx)] = err
			return err
		})
	}
	statusError := statusGroup.Wait()
	var err error
	if statusError != nil {
		err = statusError.ErrorOrNil()
	}
	return status, err
}

// ForEach runs the called function once for every node in the farm and
// collects their results, continuing until it finishes visiting every node or
// a function call returns true as its first return value.
func (f *Farm) ForEach(ctx context.Context, fn func(context.Context, string, ImageBuilder) (bool, error)) error {
	var merr *multierror.Error
	for name, builder := range f.Builders {
		stop, err := fn(ctx, name, builder)
		if err != nil {
			merr = multierror.Append(merr, fmt.Errorf("%s: %w", builder.Name(ctx), err))
		}
		if stop {
			break
		}
	}
	var err error
	if merr != nil {
		err = merr.ErrorOrNil()
	}
	return err
}

// NativePlatforms returns a list of the set of platforms for which the farm
// can build images natively.
func (f *Farm) NativePlatforms(ctx context.Context) ([]string, error) {
	options := InfoOptions{}
	nativeMap := make(map[string]struct{})
	var nativeMutex sync.Mutex
	var nativeGroup multierror.Group
	for _, builder := range f.Builders {
		builder := builder
		nativeGroup.Go(func() error {
			logrus.Debugf("getting native platform of %q", builder.Name(ctx))
			defer logrus.Debugf("got native platform of %q", builder.Name(ctx))
			platform, err := builder.NativePlatform(ctx, options)
			if err != nil {
				return err
			}
			nativeMutex.Lock()
			defer nativeMutex.Unlock()
			nativeMap[platform] = struct{}{}
			return nil
		})
	}
	merr := nativeGroup.Wait()
	if merr != nil {
		if err := merr.ErrorOrNil(); err != nil {
			return nil, err
		}
	}
	var platforms []string
	for platform := range nativeMap {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return platforms, nil
}

// EmulatedPlatforms returns a list of the set of platforms for which the farm
// can build images with the help of emulation.
func (f *Farm) EmulatedPlatforms(ctx context.Context) ([]string, error) {
	options := InfoOptions{}
	emulatedMap := make(map[string]struct{})
	var emulatedMutex sync.Mutex
	var emulatedGroup multierror.Group
	for _, builder := range f.Builders {
		builder := builder
		emulatedGroup.Go(func() error {
			logrus.Debugf("getting emulated platforms of %q", builder.Name(ctx))
			defer logrus.Debugf("got emulated platforms of %q", builder.Name(ctx))
			emulatedPlatforms, err := builder.EmulatedPlatforms(ctx, options)
			if err != nil {
				return err
			}
			emulatedMutex.Lock()
			defer emulatedMutex.Unlock()
			for _, platform := range emulatedPlatforms {
				emulatedMap[platform] = struct{}{}
			}
			return nil
		})
	}
	merr := emulatedGroup.Wait()
	if merr != nil {
		if err := merr.ErrorOrNil(); err != nil {
			return nil, err
		}
	}
	var platforms []string
	for platform := range emulatedMap {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return platforms, nil
}

// Schedule takes a list of platforms and returns a list of connections which
// can be used to build for those platforms.  It always prefers native builders
// over emulated builders, but will assign a builder which can use emulation
// for a platform if no suitable native builder is available.
//
// If platforms is an empty list, all available native platforms will be
// scheduled.
//
// TODO: add (Priority,Weight *int) a la RFC 2782 to Connections and factor
// them in when assigning builds to nodes in here.
func (f *Farm) Schedule(ctx context.Context, platforms []string) (Schedule, error) {
	var err error
	// If we weren't given a list of target platforms, generate one.
	if len(platforms) == 0 {
		platforms, err = f.NativePlatforms(ctx)
		if err != nil {
			return Schedule{}, fmt.Errorf("reading list of available native platforms: %w", err)
		}
	}
	platformBuilders := make(map[string]string)
	native := make(map[string]string)
	emulated := make(map[string]string)
	// Make notes of which platforms we can build for natively, and which
	// ones we can build for using emulation.
	var infoGroup multierror.Group
	var infoMutex sync.Mutex
	for name, builder := range f.Builders {
		name, builder := name, builder
		infoGroup.Go(func() error {
			info, err := builder.Info(ctx, InfoOptions{})
			if err != nil {
				return err
			}
			infoMutex.Lock()
			defer infoMutex.Unlock()
			if _, assigned := native[info.NativePlatform]; !assigned {
				native[info.NativePlatform] = name
			}
			for _, e := range info.EmulatedPlatforms {
				if _, assigned := emulated[e]; !assigned {
					emulated[e] = name
				}
			}
			return nil
		})
	}
	merr := infoGroup.Wait()
	if merr != nil {
		if err := merr.ErrorOrNil(); err != nil {
			return Schedule{}, err
		}
	}
	// Assign a build to the first node that could build it natively, and
	// if there isn't one, the first one that can build it with the help of
	// emulation, and if there aren't any, error out.
	for _, platform := range platforms {
		if builder, ok := native[platform]; ok {
			platformBuilders[platform] = builder
		} else if builder, ok := emulated[platform]; ok {
			platformBuilders[platform] = builder
		} else {
			return Schedule{}, fmt.Errorf("no builder capable of building for platform %q available", platform)
		}
	}
	schedule := Schedule{
		PlatformBuilders: platformBuilders,
	}
	return schedule, nil
}

// Build runs a build using the specified targetplatform:service map.  If all
// builds succeed, it copies the resulting images from the remote hosts to the
// local service and builds a manifest list with the specified reference name.
func (f *Farm) Build(ctx context.Context, reference string, schedule Schedule, containerFiles []string, options entities.BuildOptions) error {
	switch options.OutputFormat {
	default:
		return fmt.Errorf("unknown output format %q requested", options.OutputFormat)
	case "", define.OCIv1ImageManifest:
		options.OutputFormat = define.OCIv1ImageManifest
	case define.Dockerv2ImageManifest:
	}

	// Build the list of jobs.
	var jobs sync.Map
	type job struct {
		platform string
		os       string
		arch     string
		variant  string
		builder  ImageBuilder
	}
	for platform, builderName := range schedule.PlatformBuilders { // prepare to build
		builder, ok := f.Builders[builderName]
		if !ok {
			return fmt.Errorf("unknown builder %q", builderName)
		}
		var rawOS, rawArch, rawVariant string
		p := strings.Split(platform, "/")
		if len(p) > 0 && p[0] != "" {
			rawOS = p[0]
		}
		if len(p) > 1 {
			rawArch = p[1]
		}
		if len(p) > 2 {
			rawVariant = p[2]
		}
		os, arch, variant := libimage.NormalizePlatform(rawOS, rawArch, rawVariant)
		jobs.Store(builderName, job{
			platform: platform,
			os:       os,
			arch:     arch,
			variant:  variant,
			builder:  builder,
		})
	}

	// Decide where the final result will be stored.
	var listBuilder ListBuilder
	var err error
	listBuilderOptions := ListBuilderOptions{
		ForceRemoveIntermediates: options.ForceRmIntermediateCtrs,
		RemoveIntermediates:      options.RemoveIntermediateCtrs,
		IIDFile:                  options.IIDFile,
	}
	if strings.HasPrefix(reference, "dir:") || f.storeOptions == nil {
		location := strings.TrimPrefix(reference, "dir:")
		listBuilder, err = NewFileListBuilder(location, listBuilderOptions)
	} else {
		listBuilder, err = NewPodmanLocalListBuilder(reference, f.FlagSet, f.storeOptions, listBuilderOptions)
	}
	if err != nil {
		return fmt.Errorf("preparing to build list: %w", err)
	}

	// Start builds in parallel and wait for them all to finish.
	var buildResults sync.Map
	var buildGroup multierror.Group
	type buildResult struct {
		report  BuildReport
		builder ImageBuilder
	}
	for platform, builder := range schedule.PlatformBuilders {
		platform, builder := platform, builder
		outReader, outWriter := io.Pipe()
		errReader, errWriter := io.Pipe()
		go func() {
			defer outReader.Close()
			reader := bufio.NewReader(outReader)
			writer := options.Out
			if writer == nil {
				writer = os.Stdout
			}
			line, err := reader.ReadString('\n')
			for err == nil {
				line = strings.TrimSuffix(line, "\n")
				fmt.Fprintf(writer, "[%s@%s] %s\n", platform, builder, line)
				line, err = reader.ReadString('\n')
			}
		}()
		go func() {
			defer errReader.Close()
			reader := bufio.NewReader(errReader)
			writer := options.Err
			if writer == nil {
				writer = os.Stderr
			}
			line, err := reader.ReadString('\n')
			for err == nil {
				line = strings.TrimSuffix(line, "\n")
				fmt.Fprintf(writer, "[%s@%s] %s\n", platform, builder, line)
				line, err = reader.ReadString('\n')
			}
		}()
		buildGroup.Go(func() error {
			var j job
			defer outWriter.Close()
			defer errWriter.Close()
			c, ok := jobs.Load(builder)
			if !ok {
				return fmt.Errorf("unknown connection for %q (shouldn't happen)", builder)
			}
			if j, ok = c.(job); !ok {
				return fmt.Errorf("unexpected connection type for %q (shouldn't happen)", builder)
			}
			theseOptions := options
			theseOptions.IIDFile = ""
			theseOptions.Platforms = []struct{ OS, Arch, Variant string }{{j.os, j.arch, j.variant}}
			theseOptions.Out = outWriter
			theseOptions.Err = errWriter
			logrus.Infof("starting build for %v at %q", theseOptions.Platforms, builder)
			buildReport, err := j.builder.Build(ctx, "", containerFiles, theseOptions)
			if err != nil {
				return fmt.Errorf("building for %q on %q: %w", j.platform, builder, err)
			}
			logrus.Infof("finished build for %v at %q: built %s", theseOptions.Platforms, builder, buildReport.ImageID)
			buildResults.Store(platform, buildResult{
				report:  buildReport,
				builder: j.builder,
			})
			return nil
		})
	}
	buildErrors := buildGroup.Wait()
	if err := buildErrors.ErrorOrNil(); err != nil {
		return fmt.Errorf("building: %w", err)
	}

	// Assemble the final result.
	perArchBuilds := make(map[BuildReport]ImageBuilder)
	buildResults.Range(func(k, v any) bool {
		result, ok := v.(buildResult)
		if !ok {
			fmt.Fprintf(os.Stderr, "report %v not a build result?", v)
			return false
		}
		perArchBuilds[result.report] = result.builder
		return true
	})
	location, err := listBuilder.Build(ctx, perArchBuilds)
	if err != nil {
		return err
	}
	logrus.Infof("saved list to %q", location)
	return nil
}
