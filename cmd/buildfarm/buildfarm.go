package main

import (
	"context"
	"fmt"
	"os"

	"github.com/containers/podman/v4/pkg/domain/entities"
	"github.com/containers/storage"
	"github.com/containers/storage/pkg/reexec"
	"github.com/containers/storage/pkg/unshare"
	"github.com/nalind/buildfarm"
)

func main() {
	if reexec.Init() {
		return
	}
	unshare.MaybeReexecUsingUserNamespace(true)

	var storeOptions *storage.StoreOptions
	defaultStoreOptions, err := storage.DefaultStoreOptionsAutoDetectUID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "selecting storage options: %v", err)
		return
	}
	storeOptions = &defaultStoreOptions

	// logrus.SetLevel(logrus.DebugLevel)

	ctx := context.TODO()
	farm, err := buildfarm.GetFarm(ctx, "", storeOptions, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initializing: %v\n", err)
		return
	}
	status, err := farm.Status(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "status: %v\n", status)

	platforms, err := farm.NativePlatforms(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platforms: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "native platforms: %v\n", platforms)
	platforms, err = farm.EmulatedPlatforms(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platforms: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "emulated platforms: %v\n", platforms)

	schedule, err := farm.Schedule(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schedule: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "schedule: %v\n", schedule)

	var buildOptions entities.BuildOptions
	buildOptions.ContextDirectory = "."
	buildOptions.Layers = true
	err = farm.Build(ctx, "localhost/test", schedule, nil, buildOptions)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Fprintf(os.Stderr, "build: ok\n")
}
