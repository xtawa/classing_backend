//go:build windows

package httpapi

import (
	"math"
)

func storageAvailableBytes(string) (uint64, error) { return math.MaxUint64, nil }

func syncStorageDirectory(string) error { return nil }
