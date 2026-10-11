package control

import "errors"

// ErrDaemonRunning means a live daemon already owns the socket.
var ErrDaemonRunning = errors.New("a node daemon is already running")

// ErrNotRunning means no daemon is listening on the client's socket.
var ErrNotRunning = errors.New("node daemon is not running")

// ErrInvalidConfig can be wrapped by Backend.Reload when the new config
// cannot be applied. The server maps it to HTTP 400.
var ErrInvalidConfig = errors.New("invalid node config")
