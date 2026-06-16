package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// FallbackBackend wraps an ordered list of backends, trying them sequentially
// if one fails. This transparently handles rate limits or API outages.
type FallbackBackend struct {
	backends []Backend
}

// NewFallbackBackend creates a FallbackBackend from one or more backends.
// If only one backend is provided, it is returned unwrapped.
// It panics if no backends are provided.
func NewFallbackBackend(backends ...Backend) Backend {
	if len(backends) == 0 {
		panic("NewFallbackBackend requires at least one backend")
	}
	if len(backends) == 1 {
		return backends[0]
	}
	return &FallbackBackend{backends: backends}
}

// Name identifies the fallback chain.
func (f *FallbackBackend) Name() string {
	names := make([]string, len(f.backends))
	for i, b := range f.backends {
		names[i] = b.Name()
	}
	return "fallback(" + strings.Join(names, " -> ") + ")"
}

// Run tries each backend in order. If a backend fails, it moves to the next.
// If all backends fail, it returns an aggregated error.
func (f *FallbackBackend) Run(ctx context.Context, task Task) (Result, error) {
	var errs []error
	for _, b := range f.backends {
		res, err := b.Run(ctx, task)
		if err == nil {
			return res, nil
		}
		// If context is canceled by the caller (e.g. timeout or abort),
		// we should not retry the other fallbacks.
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("[%s] %w", b.Name(), err))
	}

	var sb strings.Builder
	sb.WriteString("all backends failed:")
	for _, e := range errs {
		sb.WriteString(" " + e.Error() + ";")
	}
	return Result{}, errors.New(strings.TrimRight(sb.String(), ";"))
}
