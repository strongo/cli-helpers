// Package daemonlifecycle provides the small cross-platform primitives shared
// by CLI daemons: owner-only state paths and advisory file locking.
//
// It deliberately does not own a daemon's state machine, process launching, or
// recovery policy. Those remain product decisions. This package only keeps the
// security- and OS-sensitive mechanics identical across CLI implementations.
package daemonlifecycle
