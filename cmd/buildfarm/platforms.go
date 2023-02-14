package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/nalind/buildfarm"
	"github.com/spf13/cobra"
)

var (
	platformsDescription = `Checks the platforms of podman system connections.`
	platformsCommand     = &cobra.Command{
		Use:     "platforms",
		Short:   "Check on platforms of the nodes in a build farm",
		Long:    platformsDescription,
		RunE:    platformsCmd,
		Example: "  platforms [flags] [farm]",
		Args:    cobra.MaximumNArgs(1),
	}
)

func platformsCmd(cmd *cobra.Command, args []string) error {
	ctx := context.TODO()
	farmName := ""
	if len(args) > 0 {
		farmName = args[0]
	}
	farm, err := buildfarm.NewFarm(ctx, farmName, globalStorageOptions, nil)
	if err != nil {
		return fmt.Errorf("initializing: %w", err)
	}
	globalFarm = farm
	nativePlatforms, err := farm.NativePlatforms(ctx)
	if err != nil {
		return fmt.Errorf("getting native platforms: %w", err)
	}
	emulatedPlatforms, err := farm.EmulatedPlatforms(ctx)
	if err != nil {
		return fmt.Errorf("getting emulated platforms: %w", err)
	}
	fmt.Printf("Native: ")
	if len(nativePlatforms) == 0 {
		fmt.Printf("(none)\n")
	} else {
		sort.Strings(nativePlatforms)
		for i, p := range nativePlatforms {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%s", p)
		}
		fmt.Print("\n")
	}
	fmt.Printf("Emulated: ")
	if len(emulatedPlatforms) == 0 {
		fmt.Printf("(none)\n")
	} else {
		sort.Strings(emulatedPlatforms)
		for i, p := range emulatedPlatforms {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%s", p)
		}
		fmt.Print("\n")
	}
	return nil
}

func init() {
	mainCmd.AddCommand(platformsCommand)
}
