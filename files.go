package buildfarm

import (
	"context"
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/containers/common/libimage/manifests"
	"github.com/containers/common/pkg/supplemented"
	cp "github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	"github.com/hashicorp/go-multierror"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type listFiles struct {
	directory string
	options   ListBuilderOptions
}

func NewFileListBuilder(directory string, options ListBuilderOptions) (ListBuilder, error) {
	return &listFiles{directory: directory, options: options}, nil
}

func (m *listFiles) Build(ctx context.Context, images map[BuildReport]ImageBuilder) error {
	listFormat := v1.MediaTypeImageIndex
	imageFormat := v1.MediaTypeImageManifest
	var sys types.SystemContext

	defaultPolicy, err := signature.DefaultPolicy(&sys)
	if err != nil {
		return nil
	}
	policyContext, err := signature.NewPolicyContext(defaultPolicy)
	if err != nil {
		return nil
	}

	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tempDir)
	name := fmt.Sprintf("dir:%s", tempDir)
	tempRef, err := alltransports.ParseImageName(name)
	if err != nil {
		return fmt.Errorf("parsing temporary image ref %q: %w", name, err)
	}
	output, err := alltransports.ParseImageName("dir:" + m.directory)
	if err != nil {
		return fmt.Errorf("parsing output directory ref %q: %w", "dir:"+m.directory, err)
	}

	list := manifests.Create()

	// pull the images into the temporary directory
	var pullGroup multierror.Group
	refs := make(map[BuildReport]types.ImageReference)
	var refsMutex sync.Mutex
	for image, engine := range images {
		image := image
		engine := engine
		tempFile, err := ioutil.TempFile(tempDir, "archive-*.tar")
		if err != nil {
			return err
		}
		defer tempFile.Close()
		pullGroup.Go(func() error {
			pullOptions := PullToFileOptions{
				ImageID:    image.ImageID,
				SaveFormat: image.SaveFormat,
				SaveFile:   tempFile.Name(),
			}
			if image.SaveFormat == manifest.DockerV2Schema2MediaType {
				listFormat = manifest.DockerV2ListMediaType
				imageFormat = manifest.DockerV2Schema2MediaType
			}
			reference, err := engine.PullToFile(ctx, pullOptions)
			if err != nil {
				return fmt.Errorf("pulling image %q to temporary directory: %w", image, err)
			}
			ref, err := alltransports.ParseImageName(reference)
			if err != nil {
				return fmt.Errorf("pulling image %q to temporary directory: %w", image, err)
			}
			refsMutex.Lock()
			defer refsMutex.Unlock()
			refs[image] = ref
			return nil
		})
	}
	pullErrors := pullGroup.Wait()
	err = pullErrors.ErrorOrNil()
	if err != nil {
		return fmt.Errorf("building: %w", err)
	}

	if m.options.RemoveIntermediates {
		var rmGroup multierror.Group
		for image, engine := range images {
			image := image
			engine := engine
			rmGroup.Go(func() error {
				return engine.RemoveImage(ctx, RemoveImageOptions{ImageID: image.ImageID})
			})
		}
		rmErrors := rmGroup.Wait()
		if rmErrors != nil {
			if err = rmErrors.ErrorOrNil(); err != nil {
				return fmt.Errorf("removing intermediate images: %w", err)
			}
		}
	}

	// add the images to the list
	var supplemental []types.ImageReference
	for image, ref := range refs {
		if _, err := list.Add(ctx, &sys, ref, true); err != nil {
			return fmt.Errorf("adding image %q to list: %w", image.ImageID, err)
		}
		supplemental = append(supplemental, ref)
	}

	// save the list to the temporary directory to be the main manifest
	listBytes, err := list.Serialize(listFormat)
	if err != nil {
		return fmt.Errorf("serializing manifest list: %w", err)
	}
	if err = ioutil.WriteFile(filepath.Join(tempDir, "manifest.json"), listBytes, fs.FileMode(0o600)); err != nil {
		return fmt.Errorf("writing temporary manifest list: %w", err)
	}

	// now copy everything to the final dir: location
	input := supplemented.Reference(tempRef, supplemental, cp.CopyAllImages, nil)
	copyOptions := cp.Options{
		ForceManifestMIMEType: imageFormat,
		ImageListSelection:    cp.CopyAllImages,
	}
	_, err = cp.Image(ctx, policyContext, output, input, &copyOptions)
	if err != nil {
		return fmt.Errorf("copying images to dir:%q: %w", m.directory, err)
	}
	return nil
}
