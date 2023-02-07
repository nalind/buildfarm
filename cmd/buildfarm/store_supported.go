//go:build linux
// +build linux

package main

import (
	"fmt"
	"os"

	"github.com/containers/storage"
)

var globalStore storage.Store

func init() {
	if defaultStoreOptions, err := storage.DefaultStoreOptionsAutoDetectUID(); err == nil {
		globalStorageOptions = &defaultStoreOptions
	}
}

func getDefaultStoreOptions() *storage.StoreOptions {
	defaultStoreOptions, err := storage.DefaultStoreOptionsAutoDetectUID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "selecting storage options: %v", err)
		return nil
	}
	globalStorageOptions = &defaultStoreOptions
	return &defaultStoreOptions
}

func storeBefore() error {
	storeOptions := getDefaultStoreOptions()
	if storeOptions != nil {
		store, err := storage.GetStore(*storeOptions)
		if err != nil {
			return err
		}
		globalStore = store
	}
	return nil
}

func storeAfter() error {
	if globalStore != nil {
		_, err := globalStore.Shutdown(false)
		return err
	}
	return nil
}
