package buildfarm

import (
	"context"

	"github.com/containers/podman/v4/pkg/domain/entities"
)

type ImageBuilder interface {
	WithEngine(ctx context.Context, fn func(ctx context.Context, engine entities.ImageEngine) error) error
	Info(ctx context.Context, options InfoOptions) (*Info, error)
	Status(ctx context.Context) error
	Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error)
	PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error)
	PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error)
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

type ListBuilder interface {
	Build(ctx context.Context, images map[BuildReport]ImageBuilder) error
}
