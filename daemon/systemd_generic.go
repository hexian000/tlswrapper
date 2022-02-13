//go:build !linux

package daemon

import "errors"

func Notify(state string) (bool, error) {
	return false, errors.New("systemd is not supported on current platform")
}
