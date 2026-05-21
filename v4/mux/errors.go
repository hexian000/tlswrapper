// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import "errors"

var (
	// ErrSessionClosed is returned by Accept and Open when the session has been closed.
	ErrSessionClosed = errors.New("session closed")

	// ErrNoDeadline is returned when deadline operations are not supported.
	ErrNoDeadline = errors.New("deadline not supported")
)
