package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/containers/storage"
	"github.com/containers/storage/pkg/reexec"
	"github.com/containers/storage/pkg/unshare"
	"github.com/nalind/buildfarm"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	globalStorageOptions *storage.StoreOptions

	globalFarm *buildfarm.Farm

	mainCmd = &cobra.Command{
		Use:  "buildfarm",
		Long: "A tool that builds container images for multiple architectures",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return before(cmd)
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			return after(cmd)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	globalSettings struct {
		debug          bool
		local          bool
		farmName       string
		adHocFarmNodes []string
	}
)

func before(cmd *cobra.Command) error {
	if globalSettings.debug {
		logrus.SetLevel(logrus.DebugLevel)
	}
	if err := storeBefore(); err != nil {
		return err
	}
	return nil
}

func after(cmd *cobra.Command) error {
	if globalFarm != nil {
		if err := globalFarm.Done(context.TODO()); err != nil {
			return err
		}
	}
	if err := storeAfter(); err != nil {
		return err
	}
	return nil
}

func getFarm(ctx context.Context) (farm *buildfarm.Farm, err error) {
	if len(globalSettings.adHocFarmNodes) > 0 {
		farm, err = buildfarm.NewAdHocFarm(ctx, globalSettings.adHocFarmNodes, globalStorageOptions, nil)
	} else {
		farm, err = buildfarm.NewFarm(ctx, globalSettings.farmName, globalStorageOptions, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("initializing: %w", err)
	}
	return farm, err
}

func main() {
	if reexec.Init() {
		return
	}
	unshare.MaybeReexecUsingUserNamespace(true)

	exitCode := 1

	mainCmd.PersistentFlags().StringVar(&globalSettings.farmName, "farm", "", "name of farm to use")
	mainCmd.PersistentFlags().StringSliceVar(&globalSettings.adHocFarmNodes, "node", nil, "name of node to use in an ad-hoc farm")
	mainCmd.PersistentFlags().BoolVar(&globalSettings.debug, "debug", false, "print debugging information")
	mainCmd.PersistentFlags().BoolVar(&globalSettings.local, "local", false, "include local builder")

	err := mainCmd.Execute()
	if err != nil {
		if logrus.IsLevelEnabled(logrus.TraceLevel) {
			fmt.Fprintf(os.Stderr, "Error: %+v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if w, ok := ee.Sys().(syscall.WaitStatus); ok {
				exitCode = w.ExitStatus()
			}
		}
	} else {
		exitCode = 0
	}
	os.Exit(exitCode)
}
