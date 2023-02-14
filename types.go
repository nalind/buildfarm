package buildfarm

import (
	"context"

	"github.com/containers/podman/v4/pkg/domain/entities"
)

type ImageBuilder interface {
	Driver(ctx context.Context) string
	Name(ctx context.Context) string
	Status(ctx context.Context) error
	Info(ctx context.Context, options InfoOptions) (*Info, error)
	NativePlatform(ctx context.Context, options InfoOptions) (string, error)
	EmulatedPlatforms(ctx context.Context, options InfoOptions) ([]string, error)
	Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error)
	PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error)
	PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error)
	RemoveImage(ctx context.Context, options RemoveImageOptions) error
	PruneImages(ctx context.Context, options PruneImageOptions) (PruneImageReport, error)
	Done(ctx context.Context) error
}

type InfoOptions struct {
}

type Info struct {
	NativePlatform    string
	EmulatedPlatforms []string
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
}

type PruneImageReport struct {
	ImageIDs []string
}

type ListBuilderOptions struct {
	ForceRemoveIntermediates bool
	RemoveIntermediates      bool
	IIDFile                  string
}

type ListBuilder interface {
	Build(ctx context.Context, images map[BuildReport]ImageBuilder) (string, error)
}
