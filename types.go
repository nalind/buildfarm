package buildfarm

import (
	"context"
	"io"

	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/podman/v4/pkg/domain/entities"
)

// ImageBuilder is a subset of the entities.ImageEngine interface.
type ImageBuilder interface {
	Driver(ctx context.Context) string
	Name(ctx context.Context) string
	Status(ctx context.Context) error
	Info(ctx context.Context, options InfoOptions) (*Info, error)
	NativePlatforms(ctx context.Context, options InfoOptions) ([]string, error)
	EmulatedPlatforms(ctx context.Context, options InfoOptions) ([]string, error)
	Build(ctx context.Context, reference string, containerFiles []string, options BuildOptions) (BuildReport, error)
	PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error)
	PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error)
	RemoveImage(ctx context.Context, options RemoveImageOptions) error
	PruneImages(ctx context.Context, options PruneImageOptions) (PruneImageReport, error)
	Done(ctx context.Context) error
}

type InfoOptions struct {
}

type Info struct {
	NativePlatforms   []string
	EmulatedPlatforms []string
}

type BuildOptions struct {
	OutputFormat                      string
	Out                               io.Writer
	Err                               io.Writer
	ForceRemoveIntermediateContainers bool
	RemoveIntermediateContainers      bool
	RemoveIntermediateImages          bool
	PruneImagesOnSuccess              bool
	Platforms                         []struct{ OS, Arch, Variant string }
	Pull                              bool
	IIDFile                           string
	ContextDirectory                  string
	Labels                            []string
	Args                              map[string]string
	ShmSize                           string
	Ulimit                            []string
	Memory                            int64
	MemorySwap                        int64
	CPUShares                         uint64
	CPUQuota                          int64
	CPUPeriod                         uint64
	CPUSetCPUs                        string
	CPUSetMems                        string
	CgroupParent                      string
	NoCache                           bool
	Quiet                             bool
	CacheFrom                         []reference.Named
	CacheTo                           []reference.Named
	AddHost                           []string
	Target                            string
	ConfigureNetwork                  *bool
}

type BuildReport struct {
	ImageID    string
	SaveFormat string
}

type PullToFileOptions struct {
	ImageID    string
	SaveFormat string
	SaveFile   string
}

type PullToLocalOptions struct {
	ImageID     string
	SaveFormat  string
	Destination entities.ImageEngine
}

type RemoveImageOptions struct {
	ImageID string
}

type PruneImageOptions struct {
	All bool
}

type PruneImageReport struct {
	ImageIDs   []string
	ImageNames []string
}

type ListBuilderOptions struct {
	ForceRemoveIntermediateContainers bool
	RemoveIntermediateContainers      bool
	RemoveIntermediateImages          bool
	PruneImagesOnSuccess              bool
	IIDFile                           string
}

type ListBuilder interface {
	Build(ctx context.Context, images map[BuildReport]ImageBuilder) (string, error)
}
