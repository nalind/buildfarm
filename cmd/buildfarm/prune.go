package main

import (
	"context"
	"fmt"

	"github.com/nalind/buildfarm"
	"github.com/spf13/cobra"
)

var (
	pruneDescription = `Prunes untagged and optionall unused images from a build farm.`
	pruneCommand     = &cobra.Command{
		Use:     "prune",
		Short:   "Prunes untagged and optionall unused images on the nodes in a build farm",
		Long:    pruneDescription,
		RunE:    pruneCmd,
		Example: "  prune [flags] [farm]",
		Args:    cobra.MaximumNArgs(1),
	}
	pruneOptions buildfarm.PruneImageOptions
)

func pruneCmd(cmd *cobra.Command, args []string) error {
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
	pruneReport, err := farm.PruneImages(ctx, pruneOptions)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	for name, report := range pruneReport {
		for _, tag := range report.ImageNames {
			fmt.Printf("%s: %s\n", name, tag)
		}
		for _, imageID := range report.ImageIDs {
			fmt.Printf("%s: %s\n", name, imageID)
		}
	}
	return nil
}

func init() {
	mainCmd.AddCommand(pruneCommand)
	pruneCommand.PersistentFlags().BoolVar(&pruneOptions.All, "all", false, "remove unused images")
}
