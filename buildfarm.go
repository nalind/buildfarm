package buildfarm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/containers/buildah/define"
	"github.com/containers/buildah/pkg/parse"
	"github.com/containers/common/libimage"
	"github.com/containers/common/libimage/manifests"
	"github.com/containers/common/pkg/config"
	"github.com/containers/common/pkg/supplemented"
	cp "github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/signature"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/system"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/podman/v4/pkg/domain/infra"
	"github.com/containers/storage"
	"github.com/hashicorp/go-multierror"
	"github.com/nalind/buildfarm/emulation"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
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
	Driver            string   // empty/"local" or "podman-remote"
	Name              string   // empty -> local podman
	NativePlatform    string   // set when we polled it by calling a Farm's Status() method
	EmulatedPlatforms []string // valid if NativePlatform is set
}

// CreateFarm creates an empty farm which will use a set of system connections
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
	if farm.storeOptions != nil { // make a shallow copy - could/should be a deep copy?
		options := *storeOptions
		farm.storeOptions = &options
	}
	for dest := range custom.Engine.ServiceDestinations {
		farm.Connections = append(farm.Connections, Connection{
			Name:   dest,
			Driver: "podman-remote",
		})
	}
	if len(farm.Connections) > 0 {
		return farm, nil // note that we can use the local engine
	}
	return nil, errors.New("no remote connections configured")
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
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	status := make(map[string]error)
	if f.storeOptions != nil {
		status[""] = nil
	}
	var statusError *multierror.Error
	for i := range f.Connections {
		connctx := ctx
		conn := &f.Connections[i]
		switch f.Connections[i].Driver {
		case "", "local":
			status[conn.Name] = nil
			conn.NativePlatform = parse.DefaultPlatform()
			break
		case "podman-local":
			dest, ok := custom.Engine.ServiceDestinations[conn.Name]
			if !ok {
				err := fmt.Errorf("attempted to use destination %q, but no such destination exists", conn)
				status[conn.Name] = err
				statusError = multierror.Append(statusError, err)
				continue
			}
			if dest.Identity != "" {
				connctx, err = bindings.NewConnectionWithIdentity(ctx, dest.URI, dest.Identity, dest.IsMachine)
				if err != nil {
					err := fmt.Errorf("connecting to %q at %q as %q: %w", conn.Name, dest.URI, dest.Identity, err)
					status[conn.Name] = err
					statusError = multierror.Append(statusError, err)
					continue
				}
			} else {
				connctx, err = bindings.NewConnection(ctx, dest.URI)
				if err != nil {
					err := fmt.Errorf("connecting to %q at %q: %w", conn.Name, dest.URI, err)
					status[conn.Name] = err
					statusError = multierror.Append(statusError, err)
					continue
				}
			}
			info, err := system.Info(connctx, &system.InfoOptions{})
			if err != nil {
				err := fmt.Errorf("retrieving host info from %q: %w", conn.Name, err)
				status[conn.Name] = err
				statusError = multierror.Append(statusError, err)
				continue
			}
			conn.NativePlatform = info.Host.OS + "/" + info.Host.Arch // TODO: pester someone about returning variant info
		}
	}
	err = nil
	if statusError != nil {
		err = statusError.ErrorOrNil()
	}
	return status, err
}

// NativePlatforms returns a list of the set of platforms for which the farm
// can build images natively.
func (f *Farm) NativePlatforms(ctx context.Context) ([]string, error) {
	_, err := f.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading remote status: %w", err)
	}
	var platforms []string
	if f.storeOptions != nil {
		platforms = append(platforms, parse.DefaultPlatform())
	}
	for i := range f.Connections {
		platforms = append(platforms, f.Connections[i].NativePlatform)
	}
	return platforms, nil
}

// ForEach runs the called function once for every node in the farm and
// collects their results.  If the local node is configured, it is included.
func (f *Farm) ForEach(ctx context.Context, fn func(string, entities.ImageEngine) error) error {
	var merr *multierror.Error
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return fmt.Errorf("reading custom config: %w", err)
	}
	if f.storeOptions != nil {
		podmanConfig := entities.PodmanConfig{
			FlagSet:       f.FlagSet,
			EngineMode:    entities.ABIMode,
			Config:        custom,
			Runroot:       f.storeOptions.RunRoot,
			StorageDriver: f.storeOptions.GraphDriverName,
			StorageOpts:   f.storeOptions.GraphDriverOptions,
		}
		engine, err := infra.NewImageEngine(&podmanConfig)
		if err != nil {
			return fmt.Errorf("initializing local image engine: %w", err)
		}
		err = fn("", engine)
		engine.Shutdown(ctx)
		if err != nil {
			merr = multierror.Append(merr, fmt.Errorf("%s: %w", "local image engine", err))
		}
	}
	for i := range f.Connections {
		dest, ok := custom.Engine.ServiceDestinations[f.Connections[i].Name]
		if !ok {
			return fmt.Errorf("unknown node %q", f.Connections[i].Name)
		}
		remoteConfig := entities.PodmanConfig{
			FlagSet:    f.FlagSet,
			EngineMode: entities.TunnelMode,
			URI:        dest.URI,
			Identity:   dest.Identity,
		}
		remote, err := infra.NewImageEngine(&remoteConfig)
		if err != nil {
			return fmt.Errorf("initializing image engine at %q: %w", dest.URI, err)
		}
		err = fn(f.Connections[i].Name, remote)
		remote.Shutdown(ctx)
		if err != nil {
			merr = multierror.Append(merr, fmt.Errorf("%s: %w", f.Connections[i].Name, err))
		}
	}
	if merr != nil {
		err = merr.ErrorOrNil()
	}
	return err
}

// EmulatedPlatforms returns a list of the set of platforms for which the farm
// can build images with the help of emulation.
func (f *Farm) EmulatedPlatforms(ctx context.Context) ([]string, error) {
	if _, err := f.Status(ctx); err != nil {
		return nil, fmt.Errorf("reading remote status: %w", err)
	}
	var platforms []string
	knownPlatforms := make(map[string]struct{})
	if f.storeOptions != nil {
		for _, platform := range emulation.Registered() {
			if _, known := knownPlatforms[platform]; !known {
				platforms = append(platforms, platform)
				knownPlatforms[platform] = struct{}{}
			}
		}
	}
	for i := range f.Connections {
		for _, platform := range f.Connections[i].EmulatedPlatforms {
			if _, known := knownPlatforms[platform]; !known {
				platforms = append(platforms, platform)
				knownPlatforms[platform] = struct{}{}
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
func (f *Farm) Schedule(ctx context.Context, platforms []string) (map[string]string, error) {
	var err error
	if len(platforms) == 0 {
		platforms, err = f.NativePlatforms(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading list of remote native platforms: %w", err)
		}
	}
	scheduled := make(map[string]string)
	native := make(map[string]string)
	emulated := make(map[string]string)
	for i := range f.Connections {
		if f.Connections[i].NativePlatform == "" {
			if _, err := f.Status(ctx); err != nil {
				return nil, fmt.Errorf("native platform for %q is not known: %w", f.Connections[i].Name, err)
			}
			if f.Connections[i].NativePlatform == "" {
				return nil, fmt.Errorf("native platform for %q is not known: bug?", f.Connections[i].Name)
			}
		}
		if _, known := native[f.Connections[i].NativePlatform]; !known {
			native[f.Connections[i].NativePlatform] = f.Connections[i].Name
		}
		for _, e := range f.Connections[i].EmulatedPlatforms {
			if _, known := emulated[e]; !known {
				emulated[e] = f.Connections[i].Name
			}
		}
	}
	if f.storeOptions != nil {
		defaultPlatform := parse.DefaultPlatform()
		if _, known := native[defaultPlatform]; !known {
			native[defaultPlatform] = ""
		}
		for _, e := range emulation.Registered() {
			if _, known := emulated[e]; !known {
				emulated[e] = ""
			}
		}
	}
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
	var buildGroup multierror.Group
	var connections, results sync.Map
	var localEngine entities.ImageEngine
	var localRuntime *libimage.Runtime

	custom, err := config.ReadCustomConfig()
	if err != nil {
		return fmt.Errorf("reading custom config: %w", err)
	}
	if f.storeOptions != nil {
		podmanConfig := entities.PodmanConfig{
			FlagSet:       f.FlagSet,
			EngineMode:    entities.ABIMode,
			Config:        custom,
			Runroot:       f.storeOptions.RunRoot,
			StorageDriver: f.storeOptions.GraphDriverName,
			StorageOpts:   f.storeOptions.GraphDriverOptions,
		}
		localEngine, err = infra.NewImageEngine(&podmanConfig)
		if err != nil {
			return fmt.Errorf("initializing local image engine: %w", err)
		}
		defer localEngine.Shutdown(ctx)

		localRuntime, err = libimage.RuntimeFromStoreOptions(nil, f.storeOptions)
		if err != nil {
			return fmt.Errorf("initializing local manifest list storage: %w", err)
		}
		defer localRuntime.Shutdown(false)
	}

	// set up an ImageEngine for each builder
	type connection struct {
		platform string
		os       string
		arch     string
		variant  string
		engine   entities.ImageEngine
	}
	for platform, builder := range schedule { // prepare to build
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
		if _, ok := connections.Load(builder); !ok {
			platform := os + "/" + arch
			if variant != "" {
				platform += "/" + variant
			}

			if builder == "" {
				connections.Store(builder, connection{
					platform: platform,
					os:       os,
					arch:     arch,
					variant:  variant,
					engine:   localEngine,
				})
				continue
			}

			dest, ok := custom.Engine.ServiceDestinations[builder]
			if !ok {
				return fmt.Errorf("unknown builder %q", builder)
			}

			remoteConfig := entities.PodmanConfig{
				FlagSet:    f.FlagSet,
				EngineMode: entities.TunnelMode,
				URI:        dest.URI,
				Identity:   dest.Identity,
			}
			remote, err := infra.NewImageEngine(&remoteConfig)
			if err != nil {
				return fmt.Errorf("initializing image engine at %q: %w", dest.URI, err)
			}
			defer remote.Shutdown(ctx)
			connections.Store(builder, connection{
				platform: platform,
				os:       os,
				arch:     arch,
				variant:  variant,
				engine:   remote,
			})
		}
	}

	var saveOptions entities.ImageSaveOptions
	var listFormat string
	switch options.OutputFormat {
	default:
		return fmt.Errorf("unknown output format requested")
	case "", define.OCIv1ImageManifest:
		options.OutputFormat = define.OCIv1ImageManifest
		saveOptions.Format = "oci-archive"
		listFormat = v1.MediaTypeImageIndex
	case define.Dockerv2ImageManifest:
		saveOptions.Format = "docker-archive"
		listFormat = manifest.DockerV2ListMediaType
	}

	// start builds in parallel and wait for them all to finish
	for platform, builder := range schedule {
		buildGroup.Go(func() error {
			var conn connection
			c, ok := connections.Load(builder)
			if !ok {
				return fmt.Errorf("unknown connection for %q (shouldn't happen)", builder)
			}
			if conn, ok = c.(connection); !ok {
				return fmt.Errorf("unexpected connection type for %q (shouldn't happen)", builder)
			}
			theseOptions := options
			theseOptions.Platforms = []struct{ OS, Arch, Variant string }{{conn.os, conn.arch, conn.variant}}
			buildReport, err := conn.engine.Build(ctx, containerFiles, theseOptions)
			if err != nil {
				return fmt.Errorf("building for %q on %q: %w", conn.platform, builder, err)
			}
			results.Store(platform, buildReport)
			return nil
		})
	}
	buildErrors := buildGroup.Wait()
	err = buildErrors.ErrorOrNil()
	if err != nil {
		return fmt.Errorf("building: %w", err)
	}

	// need a temporary directory, no matter where things end up
	directory, err := os.MkdirTemp("", "")
	if err != nil {
		return fmt.Errorf("creating temporary directory: %w", err)
	}
	defer os.RemoveAll(directory)

	// copy the images from the builders to the temporary directory
	var copyGroup multierror.Group
	results.Range(func(k, v any) bool {
		platform, ok := k.(string)
		if !ok {
			fmt.Fprintf(os.Stderr, "platform %v not a string?", k)
			return false
		}
		report, ok := v.(*entities.BuildReport)
		if !ok {
			fmt.Fprintf(os.Stderr, "report %v not a build report?", v)
			return false
		}
		fmt.Printf("built image %q for %q\n", report.ID, platform)

		copyGroup.Go(func() error {
			imageFile := filepath.Join(directory, report.ID+".tar")

			builder := schedule[platform]
			c, ok := connections.Load(builder)
			if !ok {
				return fmt.Errorf("unknown connection for %q (shouldn't happen)", builder)
			}
			var conn connection
			if conn, ok = c.(connection); !ok {
				return fmt.Errorf("unexpected connection type for %q (shouldn't happen)", builder)
			}

			options := entities.ImageSaveOptions{
				Format: saveOptions.Format,
				Output: imageFile,
			}
			err = conn.engine.Save(ctx, report.ID, nil, options)
			if err != nil {
				return fmt.Errorf("saving image %q: %w", report.ID, err)
			}
			loadOptions := entities.ImageLoadOptions{
				Input: imageFile,
			}
			if localEngine != nil {
				// go ahead and import the image into local storage
				loadReport, err := localEngine.Load(ctx, loadOptions)
				if err != nil {
					return fmt.Errorf("loading image %q: %w", report.ID, err)
				}
				logrus.Infof("import image %q as %q", report.ID, loadReport.Names)
			}
			return nil
		})
		return true
	})
	copyErrors := copyGroup.Wait()
	err = copyErrors.ErrorOrNil()
	if err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	// add the images to a manifest list
	if localRuntime != nil {
		// create the list if we need to
		list, err := localRuntime.LookupManifestList(reference)
		if err != nil {
			list, err = localRuntime.CreateManifestList(reference)
		}
		if err != nil {
			return fmt.Errorf("creating manifest list %q: %w", reference, err)
		}
		// clear the list in case it already existed
		listContents, err := list.Inspect()
		if err != nil {
			return fmt.Errorf("inspecting list %q: %w", reference, err)
		}
		for _, instance := range listContents.Manifests {
			if err := list.RemoveInstance(instance.Digest); err != nil {
				return fmt.Errorf("removing instance %q from list %q: %w", instance.Digest, reference, err)
			}
		}
		// add the images to the list
		var addGroup multierror.Group
		var addMutex sync.Mutex
		results.Range(func(k, v any) bool {
			report, ok := v.(*entities.BuildReport)
			if !ok {
				return false
			}
			addGroup.Go(func() error {
				ref, err := istorage.Transport.ParseReference(report.ID)
				if err != nil {
					return fmt.Errorf("locating image %q to add to list: %w", report.ID, err)
				}
				name := fmt.Sprintf("%s:%s", istorage.Transport.Name(), ref.StringWithinTransport())
				var options libimage.ManifestListAddOptions
				addMutex.Lock()
				defer addMutex.Unlock()
				if _, err := list.Add(ctx, name, &options); err != nil {
					return fmt.Errorf("adding image %q to list: %w", report.ID, err)
				}
				return nil
			})
			return true
		})
		addErrors := addGroup.Wait()
		err = addErrors.ErrorOrNil()
		if err != nil {
			return fmt.Errorf("adding: %w", err)
		}
		fmt.Printf("push using `podman manifest push --all %s docker://registry/repository:tag`\n", reference)
	} else {
		// create the in-memory list
		list := manifests.Create()
		var addGroup multierror.Group
		var supplemental []types.ImageReference
		name := fmt.Sprintf("dir:%s", directory)
		mainRef, err := alltransports.ParseImageName(name)
		if err != nil {
			return fmt.Errorf("parsing image ref %q: %w", name, err)
		}
		name = fmt.Sprintf("dir:%s", reference)
		destRef, err := alltransports.ParseImageName(name)
		if err != nil {
			return fmt.Errorf("parsing image ref %q: %w", name, err)
		}
		// add the images to the list
		var addMutex sync.Mutex
		results.Range(func(k, v any) bool {
			report, ok := v.(*entities.BuildReport)
			if !ok {
				return false
			}
			addGroup.Go(func() error {
				name := fmt.Sprintf("%s:%s", saveOptions.Format, filepath.Join(directory, report.ID+".tar"))
				ref, err := alltransports.ParseImageName(name)
				if err != nil {
					return fmt.Errorf("parsing image ref %q: %w", name, err)
				}
				addMutex.Lock()
				defer addMutex.Unlock()
				if _, err := list.Add(ctx, nil, ref, true); err != nil {
					return fmt.Errorf("adding image %q to list: %w", report.ID, err)
				}
				supplemental = append(supplemental, ref)
				return nil
			})
			return true
		})
		addErrors := addGroup.Wait()
		err = addErrors.ErrorOrNil()
		if err != nil {
			return fmt.Errorf("adding: %w", err)
		}
		// save the list to the temporary directory to be the main manifest
		listBytes, err := list.Serialize(listFormat)
		if err != nil {
			return fmt.Errorf("serializing manifest list: %w", err)
		}
		if err = ioutil.WriteFile(filepath.Join(directory, "manifest.json"), listBytes, fs.FileMode(0o600)); err != nil {
			return err
		}
		// copy from the temporary directory to the output directory as the final product
		srcRef := supplemented.Reference(mainRef, supplemental, cp.CopyAllImages, nil)
		copyOptions := cp.Options{
			ImageListSelection: cp.CopyAllImages,
		}
		policy, err := signature.DefaultPolicy(nil)
		if err != nil {
			return fmt.Errorf("creating new default signature policy: %w", err)
		}
		policyContext, err := signature.NewPolicyContext(policy)
		if err != nil {
			return fmt.Errorf("creating new signature policy context: %w", err)
		}
		_, err = cp.Image(ctx, policyContext, destRef, srcRef, &copyOptions)
		if err != nil {
			return fmt.Errorf("copying: %w", err)
		}
		fmt.Printf("push using `skopeo copy --all dir:%s docker://registry/repository:tag`\n", reference)
	}

	return nil
}
