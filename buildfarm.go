package buildfarm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	storeOptions *storage.StoreOptions // not nil -> use local engine, too
	Connections  []Connection
}

// Connection represents a connection to a builder.
type Connection struct {
	Driver            string // empty/"local" or "podman-remote"
	Name              string // empty -> local podman, probably
	Builder           ImageBuilder
	NativePlatform    string   // set when we polled it by calling its Info() method
	EmulatedPlatforms []string // valid if NativePlatform is set
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

// GetDefaultFarm returns a Farm that uses all known system connections and
// which has no name.  If storeOptions is not nil, the local system will be
// included as an unnamed connection.
func GetDefaultFarm(ctx context.Context, storeOptions *storage.StoreOptions, flags *pflag.FlagSet) (*Farm, error) {
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	farm := new(Farm)
	if flags == nil {
		flags = pflag.NewFlagSet("buildfarm", pflag.ExitOnError)
	}
	farm.FlagSet = flags
	farm.storeOptions = storeOptions
	for dest := range custom.Engine.ServiceDestinations {
		ib, err := NewPodmanRemoteImageBuilder(ctx, flags, dest)
		if err != nil {
			return nil, err
		}
		farm.Connections = append(farm.Connections, Connection{
			Name:    dest,
			Driver:  "podman-remote",
			Builder: ib,
		})
	}
	if farm.storeOptions != nil { // make a shallow copy - could/should be a deep copy?
		ib, err := NewPodmanLocalImageBuilder(ctx, flags, farm.storeOptions)
		if err != nil {
			return nil, err
		}
		farm.Connections = append(farm.Connections, Connection{
			Name:    "",
			Driver:  "local",
			Builder: ib,
		})
	}
	if len(farm.Connections) > 0 {
		return farm, nil
	}
	return nil, errors.New("no builders configured")
}

// GetFarm returns a Farm that uses a configured set of system connections.
func GetFarm(ctx context.Context, name string, storeOptions *storage.StoreOptions, flags *pflag.FlagSet) (*Farm, error) {
	if name == "" {
		return GetDefaultFarm(ctx, storeOptions, flags)
	}
	return nil, errors.New("not implemented")
}

// Status polls the connections in the farm and returns a map of their
// individual status, along with an error if any are down or otherwise
// unreachable.
func (f *Farm) Status(ctx context.Context) (map[string]error, error) {
	status := make(map[string]error)
	var statusMutex sync.Mutex
	var statusGroup multierror.Group
	for _, conn := range f.Connections {
		conn := conn
		statusGroup.Go(func() error {
			logrus.Debugf("getting status of %q", conn.Name)
			err := conn.Builder.Status(ctx)
			statusMutex.Lock()
			defer statusMutex.Unlock()
			status[conn.Name] = err
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
// collects their results.  If the local node is configured, it is included.
func (f *Farm) ForEach(ctx context.Context, fn func(context.Context, string, ImageBuilder) (bool, error)) error {
	var merr *multierror.Error
	for _, conn := range f.Connections {
		stop, err := fn(ctx, conn.Name, conn.Builder)
		if err != nil {
			merr = multierror.Append(merr, fmt.Errorf("%s: %w", conn.Name, err))
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
	var infoGroup multierror.Group
	for i, conn := range f.Connections {
		i, conn := i, conn
		infoGroup.Go(func() error {
			info, err := conn.Builder.Info(ctx, InfoOptions{})
			if err != nil {
				return err
			}
			f.Connections[i].NativePlatform = info.NativePlatform
			f.Connections[i].EmulatedPlatforms = info.EmulatedPlatforms
			return nil
		})
	}
	merr := infoGroup.Wait()
	if merr != nil {
		if err := merr.ErrorOrNil(); err != nil {
			return nil, err
		}
	}
	var platforms []string
	nativeMap := make(map[string]struct{})
	for _, conn := range f.Connections {
		if _, ok := nativeMap[conn.NativePlatform]; !ok {
			platforms = append(platforms, conn.NativePlatform)
			nativeMap[conn.NativePlatform] = struct{}{}
		}
	}
	return platforms, nil
}

// EmulatedPlatforms returns a list of the set of platforms for which the farm
// can build images with the help of emulation.
func (f *Farm) EmulatedPlatforms(ctx context.Context) ([]string, error) {
	var infoGroup multierror.Group
	for i, conn := range f.Connections {
		i, conn := i, conn
		infoGroup.Go(func() error {
			info, err := conn.Builder.Info(ctx, InfoOptions{})
			if err != nil {
				return err
			}
			f.Connections[i].NativePlatform = info.NativePlatform
			f.Connections[i].EmulatedPlatforms = info.EmulatedPlatforms
			return nil
		})
	}
	merr := infoGroup.Wait()
	if merr != nil {
		if err := merr.ErrorOrNil(); err != nil {
			return nil, err
		}
	}
	var platforms []string
	emulatedMap := make(map[string]struct{})
	for _, conn := range f.Connections {
		for _, platform := range conn.EmulatedPlatforms {
			if _, ok := emulatedMap[platform]; !ok {
				platforms = append(platforms, platform)
				emulatedMap[platform] = struct{}{}
			}
		}
	}
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
func (f *Farm) Schedule(ctx context.Context, platforms []string) (map[string]string, error) {
	var err error
	// If we weren't given a list of target platforms, generate one.
	if len(platforms) == 0 {
		platforms, err = f.NativePlatforms(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading list of available native platforms: %w", err)
		}
	}
	scheduled := make(map[string]string)
	native := make(map[string]string)
	emulated := make(map[string]string)
	// Make notes of which platforms we can build for natively, and which
	// ones we can build for using emulation.
	var infoGroup multierror.Group
	var infoMutex sync.Mutex
	for i, conn := range f.Connections {
		i, conn := i, conn
		infoGroup.Go(func() error {
			info, err := conn.Builder.Info(ctx, InfoOptions{})
			if err != nil {
				return err
			}
			infoMutex.Lock()
			defer infoMutex.Unlock()
			if _, assigned := native[info.NativePlatform]; !assigned {
				native[info.NativePlatform] = conn.Name
			}
			for _, e := range f.Connections[i].EmulatedPlatforms {
				if _, assigned := emulated[e]; !assigned {
					emulated[e] = conn.Name
				}
			}
			return nil
		})
	}
	merr := infoGroup.Wait()
	if merr != nil {
		if err := merr.ErrorOrNil(); err != nil {
			return nil, err
		}
	}
	// Assign a build to the first node that could build it natively, and
	// if there isn't one, the first one that can build it with the help of
	// emulation, and if there aren't any, error out.
	for _, platform := range platforms {
		if builder, ok := native[platform]; ok {
			scheduled[platform] = builder
		} else if builder, ok := emulated[platform]; ok {
			scheduled[platform] = builder
		} else {
			return nil, fmt.Errorf("no builder capable of building for platform %q available", platform)
		}
	}
	return scheduled, nil
}

// Build runs a build using the specified targetplatform:service map.  If all
// builds succeed, it copies the resulting images from the remote hosts to the
// local service and builds a manifest list with the specified reference name.
func (f *Farm) Build(ctx context.Context, reference string, schedule map[string]string, containerFiles []string, options entities.BuildOptions) error {
	switch options.OutputFormat {
	default:
		return fmt.Errorf("unknown output format %q requested", options.OutputFormat)
	case "", define.OCIv1ImageManifest:
		options.OutputFormat = define.OCIv1ImageManifest
	case define.Dockerv2ImageManifest:
	}

	connectionByName := make(map[string]*Connection)
	for i := range f.Connections {
		connectionByName[f.Connections[i].Name] = &f.Connections[i]
	}

	// Build the list of jobs.
	var connections sync.Map
	type connection struct {
		platform string
		os       string
		arch     string
		variant  string
		builder  ImageBuilder
	}
	for platform, builderName := range schedule { // prepare to build
		builder, ok := connectionByName[builderName]
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
		connections.Store(builderName, connection{
			platform: platform,
			os:       os,
			arch:     arch,
			variant:  variant,
			builder:  builder.Builder,
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
	for platform, builder := range schedule {
		platform := platform
		builder := builder
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
			var conn connection
			defer outWriter.Close()
			defer errWriter.Close()
			c, ok := connections.Load(builder)
			if !ok {
				return fmt.Errorf("unknown connection for %q (shouldn't happen)", builder)
			}
			if conn, ok = c.(connection); !ok {
				return fmt.Errorf("unexpected connection type for %q (shouldn't happen)", builder)
			}
			theseOptions := options
			theseOptions.IIDFile = ""
			theseOptions.Platforms = []struct{ OS, Arch, Variant string }{{conn.os, conn.arch, conn.variant}}
			theseOptions.Out = outWriter
			theseOptions.Err = errWriter
			buildReport, err := conn.builder.Build(ctx, "", containerFiles, theseOptions)
			if err != nil {
				return fmt.Errorf("building for %q on %q: %w", conn.platform, builder, err)
			}
			buildResults.Store(platform, buildResult{
				report:  buildReport,
				builder: conn.builder,
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
	if err := listBuilder.Build(ctx, perArchBuilds); err != nil {
		return err
	}
	return nil
}
