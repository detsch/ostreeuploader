//go:build linux

package ostree

import (
	"errors"
	"fmt"
	"syscall"
)

// ErrInsufficientStorage is returned (wrapped) when the estimated required space
// for an update exceeds the available space after applying the reserved
// watermark. It mirrors aktualizr-lite's DownloadFailed_NoSpace and fioup's
// InsufficientStorageError.
var ErrInsufficientStorage = errors.New("insufficient storage for update")

// InsufficientStorageError carries the usage figures alongside the sentinel.
type InsufficientStorageError struct {
	Usage *UsageInfo
}

func (e *InsufficientStorageError) Error() string {
	u := e.Usage
	return fmt.Sprintf("%s: required %s, available %s; size %s, free %s, reserved %s",
		ErrInsufficientStorage.Error(),
		FormatBytes(u.Required), FormatBytes(u.Available),
		FormatBytes(u.SizeB), FormatBytes(u.Free), FormatBytes(u.Reserved))
}

func (e *InsufficientStorageError) Unwrap() error { return ErrInsufficientStorage }

// wrapENOSPC converts any error caused by ENOSPC into an ErrInsufficientStorage
// so the caller can errors.Is(err, ErrInsufficientStorage) without knowing
// whether the failure came from a pre-flight check or a mid-pull write.
func wrapENOSPC(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("%w: %w", ErrInsufficientStorage, err)
	}
	return err
}
