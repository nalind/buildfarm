package main

import (
	"context"
	"fmt"

	"github.com/nalind/buildfarm"
	"github.com/spf13/cobra"
)

var (
	pruneDescription = `Prunes unused images from a build farm.`
	pruneCommand     = &cobra.Command{
		Use:     "prune",
		Short:   "Prunes unused images on the nodes in a build farm",
		Long:    pruneDescription,
		RunE:    pruneCmd,
		Example: "  prune [flags] [farm]",
		Args:    cobra.MaximumNArgs(1),
	}
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
	pruneReport, err := farm.PruneImages(ctx, buildfarm.PruneImageOptions{})
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	for name, report := range pruneReport {
		for _, imageID := range report.ImageIDs {
			fmt.Printf("%s: %s\n", name, imageID)
		}
	}
	return nil
}

func init() {
	mainCmd.AddCommand(pruneCommand)
}
