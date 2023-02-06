package buildfarm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/containers/common/pkg/config"
	istorage "github.com/containers/image/v5/storage"
	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/storage/pkg/chrootarchive"
	docker "github.com/fsouza/go-dockerclient"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
)

type dockerEngine struct {
	name    string
	flagSet *pflag.FlagSet
	config  *config.Config
	client  *docker.Client
	os      string
	arch    string
	variant string
}

func NewDockerImageBuilder(ctx context.Context, flags *pflag.FlagSet, name string) (ImageBuilder, error) {
	if flags == nil {
		flags = pflag.NewFlagSet("buildfarm", pflag.ExitOnError)
	}
	custom, err := config.ReadCustomConfig()
	if err != nil {
		return nil, fmt.Errorf("reading custom config: %w", err)
	}
	endpoint, cert, key, ca := "", "", "", ""
	var client *docker.Client
	if cert != "" || key != "" || ca != "" {
		client, err = docker.NewTLSClient(endpoint, cert, key, ca)
	} else {
		client, err = docker.NewClient(endpoint)
	}
	if err != nil {
		return nil, err
	}
	remote := dockerEngine{
		name:    name,
		flagSet: flags,
		config:  custom,
		client:  client,
	}
	return &remote, nil
}

func (r *dockerEngine) withClient(ctx context.Context, fn func(ctx context.Context, client *docker.Client) error) error {
	return fn(ctx, r.client)
}

func (r *dockerEngine) Info(ctx context.Context, options InfoOptions) (*Info, error) {
	var nativePlatform string
	err := r.withClient(ctx, func(ctx context.Context, client *docker.Client) error {
		dockerInfo, err := client.Info()
		if err != nil {
			return fmt.Errorf("retrieving host info from %q: %w", r.name, err)
		}
		r.os = dockerInfo.OperatingSystem
		r.arch = dockerInfo.Architecture
		nativePlatform = r.os + "/" + r.arch // TODO: pester someone about returning variant info
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Info{NativePlatform: nativePlatform}, err
}

func (r *dockerEngine) Status(ctx context.Context) error {
	return r.withClient(ctx, func(ctx context.Context, client *docker.Client) error { return client.PingWithContext(ctx) })
}

func (r *dockerEngine) Build(ctx context.Context, reference string, containerFiles []string, options entities.BuildOptions) (BuildReport, error) {
	var report *entities.BuildReport
	var buildReport BuildReport
	err := r.withClient(ctx, func(ctx context.Context, client *docker.Client) error {
		dockerfile := ""
		if len(containerFiles) > 0 {
			dockerfile = containerFiles[0]
		}
		rc, err := chrootarchive.Tar(options.ContextDirectory, nil, options.ContextDirectory)
		if err != nil {
			return fmt.Errorf("archiving %q: %w", options.ContextDirectory, err)
		}
		defer rc.Close()
		buildOptions := docker.BuildImageOptions{
			Name:        reference,
			InputStream: rc,
			Platform:    r.os + "/" + r.arch,
			Context:     ctx,
			Dockerfile:  dockerfile,
		}
		if r.variant != "" {
			buildOptions.Platform += "/" + r.variant
		}
		if err := client.BuildImage(buildOptions); err != nil {
			return fmt.Errorf("building for %q/%q/%q on %q: %w", r.os, r.arch, r.variant, r.name, err)
		}
		return nil
	})
	if err != nil {
		return BuildReport{}, err
	}
	buildReport.ImageID = report.ID
	buildReport.SaveFormat = "docker-archive"
	return buildReport, nil
}

func (r *dockerEngine) PullToFile(ctx context.Context, options PullToFileOptions) (reference string, err error) {
	err = r.withClient(ctx, func(ctx context.Context, client *docker.Client) error {
		f, err := os.Create(options.SaveFile)
		if err != nil {
			return err
		}
		defer f.Close()
		defer func() {
			if err != nil {
				if e := os.Remove(f.Name()); e != nil {
					logrus.Warnf("removing %q: %v", f.Name(), e)
				}
			}
		}()
		exportOptions := docker.ExportImageOptions{
			Name:         options.ImageID,
			OutputStream: f,
			Context:      ctx,
		}
		if err := client.ExportImage(exportOptions); err != nil {
			return fmt.Errorf("saving image %q: %w", options.ImageID, err)
		}
		return nil
	})
	return options.SaveFormat + ":" + options.SaveFile, nil
}

func (r *dockerEngine) PullToLocal(ctx context.Context, options PullToLocalOptions) (reference string, err error) {
	tempFile, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()
	err = r.withClient(ctx, func(ctx context.Context, client *docker.Client) error {
		exportOptions := docker.ExportImageOptions{
			Name:         options.ImageID,
			OutputStream: tempFile,
			Context:      ctx,
		}
		if err := client.ExportImage(exportOptions); err != nil {
			return fmt.Errorf("saving image %q to temporary file: %w", options.ImageID, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	loadOptions := entities.ImageLoadOptions{
		Input: tempFile.Name(),
	}
	if options.Destination == nil {
		return "", errors.New("internal error: options.Destination not set")
	} else {
		if _, err = options.Destination.Load(ctx, loadOptions); err != nil {
			return "", fmt.Errorf("loading image %q: %w", options.ImageID, err)
		}
	}
	name := fmt.Sprintf("%s:%s", istorage.Transport.Name(), options.ImageID)
	return name, err
}
