// Package daemonlifecycle provides the small cross-platform primitives shared
// by CLI daemons: owner-only state paths, advisory file locking, detached
// process start, and process identity.
//
// It deliberately does not own a daemon's state machine, readiness protocol,
// timeouts, or recovery policy. Those remain product decisions. This package
// only keeps the security- and OS-sensitive mechanics identical across CLI
// implementations.
package daemonlifecycle
