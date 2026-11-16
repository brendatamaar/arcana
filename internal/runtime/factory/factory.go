// Package factory builds the runtime driver selected by `runtime.driver`, so the server and the
// doctor construct drivers the same way.
package factory

import (
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
)

// Driver is a runtime driver that can be closed.
type Driver interface {
	runtime.Driver
	Close() error
}

// Options carries what only the caller knows.
type Options struct {
	InstallID  string
	HelperPath string
	// Docker customises the Docker driver (orphan and egress callbacks, network settings) before it is built.
	Docker func(*docker.Config)
	// OnDecision and OnDropped receive egress proxy decisions from drivers that run proxies (containerd).
	OnDecision func(netpolicy.Decision)
	OnDropped  func(uint64)
}
