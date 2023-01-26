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
	schedule, err := farm.Schedule(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schedule: %v\n", err)
	}
	var buildOptions entities.BuildOptions
	buildOptions.ContextDirectory = "."
	buildOptions.Layers = true
	err = farm.Build(ctx, "localhost/test", schedule, nil, buildOptions)
	if err != nil {
		fmt.Println(err)
	}
}
