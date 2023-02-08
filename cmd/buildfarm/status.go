package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/nalind/buildfarm"
	"github.com/spf13/cobra"
)

var (
	statusDescription = `Checks the status of podman system connections.`
	statusCommand     = &cobra.Command{
		Use:     "status",
		Short:   "Check on status of the nodes in a build farm",
		Long:    statusDescription,
		RunE:    statusCmd,
		Example: "  status [flags] [farm]",
		Args:    cobra.MaximumNArgs(1),
	}
)

func statusCmd(cmd *cobra.Command, args []string) error {
	ctx := context.TODO()
	farmName := ""
	if len(args) > 0 {
		farmName = args[0]
	}
	farm, err := buildfarm.GetFarm(ctx, farmName, globalStorageOptions, nil)
	if err != nil {
		return fmt.Errorf("initializing: %w", err)
	}
	status, err := farm.Status(ctx)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	nodes := []string{}
	for nodeName := range status {
		nodes = append(nodes, nodeName)
	}
	sort.Strings(nodes)
	for _, nodeName := range nodes {
		err := status[nodeName]
		if nodeName == "" {
			nodeName = "local node"
		}
		if err != nil {
			fmt.Printf("%s: %v\n", nodeName, err)
			continue
		}
		fmt.Printf("%s: ready\n", nodeName)
	}
	return nil
}

func init() {
	mainCmd.AddCommand(statusCommand)
}
