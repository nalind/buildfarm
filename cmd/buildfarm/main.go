package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/containers/storage"
	"github.com/containers/storage/pkg/reexec"
	"github.com/containers/storage/pkg/unshare"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	globalStorageOptions *storage.StoreOptions

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
		debug   bool
		noLocal bool
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
	if err := storeAfter(); err != nil {
		return err
	}
	return nil
}

func main() {
	if reexec.Init() {
		return
	}
	unshare.MaybeReexecUsingUserNamespace(true)

	exitCode := 1

	mainCmd.PersistentFlags().BoolVar(&globalSettings.debug, "debug", false, "print debugging information")
	mainCmd.PersistentFlags().BoolVar(&globalSettings.noLocal, "no-local", false, "ignore local builder")

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
