//go:build !linux
// +build !linux

package main

import "github.com/containers/storage"

func storeBefore() error {
	return nil
}

func storeAfter() error {
	return nil
}
