package buildfarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/containers/buildah/define"
	"github.com/containers/common/libimage"
	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/images"
	"github.com/containers/podman/v4/pkg/bindings/system"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/storage"
	"github.com/hashicorp/go-multierror"
)

// Farm represents a group of connections to builders.
type Farm struct {
	Name       string
	Connection []Connection
}

// Connection represents a connection to a builder.
type Connection struct {
	Name              string
	NativePlatform    string   // set when we polled it by calling a Farm's Status() method
	EmulatedPlatforms []string // valid if NativePlatform is set
}

// GetDefaultFarm returns a Farm that uses all known system connections and
// which has no name.
func GetDefaultFarm(ctx context.Context) (*Farm, error) {
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	farm := new(Farm)
	for dest := range custom.Engine.ServiceDestinations {
		farm.Connection = append(farm.Connection, Connection{
			Name: dest,
		})
	}
	if len(farm.Connection) > 0 {
		return farm, nil
	}
	return nil, errors.New("no remote connections configured")
}

// GetFarm returns a Farm that uses a configured set of system connections.
func GetFarm(ctx context.Context, name string) (*Farm, error) {
	if name == "" {
		return GetDefaultFarm(ctx)
	}
	return nil, errors.New("not implemented")
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

// Status polls the connections in the farm and returns a map of their
// individual status, along with an error if any are down or otherwise
// unreachable.
func (f *Farm) Status(ctx context.Context) (map[string]error, error) {
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	status := make(map[string]error)
	var statusError *multierror.Error
	for i := range f.Connection {
		connctx := ctx
		conn := &f.Connection[i]
		status[conn.Name] = nil
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
	for i := range f.Connection {
		platforms = append(platforms, f.Connection[i].NativePlatform)
	}
	return platforms, nil
}

// EmulatedPlatforms returns a list of the set of platforms for which the farm
// can build images with the help of emulation.
func (f *Farm) EmulatedPlatforms(ctx context.Context) ([]string, error) {
	if _, err := f.Status(ctx); err != nil {
		return nil, fmt.Errorf("reading remote status: %w", err)
	}
	var platforms []string
	knownPlatforms := make(map[string]struct{})
	for i := range f.Connection {
		for _, platform := range f.Connection[i].EmulatedPlatforms {
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
	for i := range f.Connection {
		if f.Connection[i].NativePlatform == "" {
			if _, err := f.Status(ctx); err != nil {
				return nil, fmt.Errorf("native platform for %q is not known: %w", f.Connection[i].Name, err)
			}
			if f.Connection[i].NativePlatform == "" {
				return nil, fmt.Errorf("native platform for %q is not known: bug?", f.Connection[i].Name)
			}
		}
		if _, known := native[f.Connection[i].NativePlatform]; !known {
			native[f.Connection[i].NativePlatform] = f.Connection[i].Name
		}
		for _, e := range f.Connection[i].EmulatedPlatforms {
			if _, known := emulated[e]; !known {
				emulated[e] = f.Connection[i].Name
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

	storeOptions, err := storage.DefaultStoreOptionsAutoDetectUID()
	if err != nil {
		return fmt.Errorf("selecting storage options: %w", err)
	}

	runtime, err := libimage.RuntimeFromStoreOptions(nil, &storeOptions)
	if err != nil {
		return fmt.Errorf("initializing storage: %w", err)
	}

	custom, err := config.ReadCustomConfig()
	if err != nil {
		return fmt.Errorf("reading custom config: %w", err)
	}
	type connection struct {
		platform string
		os       string
		arch     string
		variant  string
		ctx      context.Context
	}
	for platform, builder := range schedule {
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
		connctx := ctx
		if _, ok := connections.Load(builder); !ok {
			dest, ok := custom.Engine.ServiceDestinations[builder]
			if !ok {
				return fmt.Errorf("unknown builder %q", builder)
			}
			if dest.Identity != "" {
				connctx, err = bindings.NewConnectionWithIdentity(ctx, dest.URI, dest.Identity, dest.IsMachine)
				if err != nil {
					return fmt.Errorf("connecting to %q at %q as %q: %w", builder, dest.URI, dest.Identity, err)
				}
			} else {
				connctx, err = bindings.NewConnection(ctx, dest.URI)
				if err != nil {
					return fmt.Errorf("connecting to %q at %q: %w", builder, dest.URI, err)
				}
			}
			platform := os + "/" + arch
			if variant != "" {
				platform += "/" + variant
			}
			connections.Store(builder, connection{
				platform: platform,
				os:       os,
				arch:     arch,
				variant:  variant,
				ctx:      connctx,
			})
		}
	}

	var exportOptions images.ExportOptions
	switch options.OutputFormat {
	default:
		return fmt.Errorf("unknown output format requested")
	case "", define.OCIv1ImageManifest:
		options.OutputFormat = define.OCIv1ImageManifest
		format := "oci-archive"
		exportOptions.Format = &format
	case define.Dockerv2ImageManifest:
		format := "docker-archive"
		exportOptions.Format = &format
	}

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
			buildReport, err := images.Build(conn.ctx, containerFiles, theseOptions)
			if err != nil {
				return fmt.Errorf("building for %q: %w", conn.platform, err)
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

	directory, err := os.MkdirTemp("", "")
	if err != nil {
		return fmt.Errorf("creating temporary directory: %w", err)
	}
	defer os.RemoveAll(directory)

	var copyGroup multierror.Group
	results.Range(func(k, v any) bool {
		platform, ok := k.(string)
		if !ok {
			fmt.Fprintf(os.Stderr, "platform %v not a string?", k)
			return false
		}
		report, ok := v.(*entities.BuildReport)
		if !ok {
			fmt.Fprintf(os.Stderr, "report %v not a report?", v)
			return false
		}
		fmt.Printf("built image %q for %q\n", report.ID, platform)

		copyGroup.Go(func() error {
			imageDirectory := filepath.Join(directory, report.ID)
			if err := os.MkdirAll(imageDirectory, 0700); err != nil {
				return fmt.Errorf("saving image %q: %w", report.ID, err)
			}
			imageFile := filepath.Join(imageDirectory, "archive.tar")

			builder := schedule[platform]
			c, ok := connections.Load(builder)
			if !ok {
				return fmt.Errorf("unknown connection for %q (shouldn't happen)", builder)
			}
			var conn connection
			if conn, ok = c.(connection); !ok {
				return fmt.Errorf("unexpected connection type for %q (shouldn't happen)", builder)
			}

			f, err := os.Create(imageFile)
			if err != nil {
				return fmt.Errorf("creating temporary image copy %q: %w", imageFile, err)
			}
			err = images.Export(conn.ctx, []string{report.ID}, f, &exportOptions)
			f.Close()
			if err != nil {
				return fmt.Errorf("exporting image %q: %w", report.ID, err)
			}
			var options libimage.LoadOptions
			if _, err = runtime.Load(ctx, imageFile, &options); err != nil {
				return fmt.Errorf("loading image %q: %w", report.ID, err)
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

	list, err := runtime.LookupManifestList(reference)
	if err != nil {
		list, err = runtime.CreateManifestList(reference)
	}
	if err != nil {
		return fmt.Errorf("creating manifest list %q: %w", reference, err)
	}
	listContents, err := list.Inspect()
	if err != nil {
		return fmt.Errorf("inspecting list %q: %w", reference, err)
	}
	for _, instance := range listContents.Manifests {
		if err := list.RemoveInstance(instance.Digest); err != nil {
			return fmt.Errorf("removing instance %q from list %q: %w", instance.Digest, reference, err)
		}
	}

	var addGroup multierror.Group
	results.Range(func(k, v any) bool {
		report, ok := v.(*entities.BuildReport)
		if !ok {
			return false
		}
		addGroup.Go(func() error {
			ref, err := istorage.Transport.ParseReference("@" + report.ID)
			if err != nil {
				return fmt.Errorf("locating image %q to add to list: %w", report.ID, err)
			}
			name := fmt.Sprintf("%s:%s", istorage.Transport.Name(), ref.StringWithinTransport())
			var options libimage.ManifestListAddOptions
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

	return nil
}
