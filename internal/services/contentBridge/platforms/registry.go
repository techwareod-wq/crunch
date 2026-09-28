package platforms

import (
	"fmt"

	"github.com/atharva-ng/crunch/internal/services/onboardingService/constants"
)

// Registry maps a publishing platform to its adapter. The one extension point:
// adding a platform means adding a Publisher and registering it here.
type Registry map[constants.PublishPlatform]Publisher

// NewRegistry builds a Registry keyed on each publisher's Platform() string.
func NewRegistry(pubs ...Publisher) Registry {
	r := make(Registry, len(pubs))
	for _, p := range pubs {
		r[constants.PublishPlatform(p.Platform())] = p
	}
	return r
}

// Get returns the publisher registered for the given platform string, or an
// error when none is registered (unknown / unconfigured platform).
func (r Registry) Get(platform string) (Publisher, error) {
	p, ok := r[constants.PublishPlatform(platform)]
	if !ok {
		return nil, fmt.Errorf("content bridge: no publisher for platform %q", platform)
	}
	return p, nil
}
