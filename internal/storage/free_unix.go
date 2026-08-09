//go:build !windows

package storage

import (
	"math"
	"syscall"
)

func FreeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blocks := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize != 0 && blocks > uint64(math.MaxInt64)/blockSize {
		return math.MaxInt64, nil
	}
	return int64(blocks * blockSize), nil
}
