package launcher

import (
	"context"
	"sync"

	"github.com/epzero/basamento/platform"
)

// Sim is a launcher simulator for the gate and for tests. It records every
// request, accepts by default and never starts anything: a scenario decides
// when a worker joins. Its LastLaunchError is the error a degraded template
// reports at the queue timeout (ADR-0012 §12).
type Sim struct {
	mu        sync.Mutex
	requests  []platform.LaunchRequest
	accept    bool
	fail      error
	lastError string
}

var _ platform.Launcher = (*Sim)(nil)

// NewSim returns a simulator that accepts every request.
func NewSim() *Sim {
	return &Sim{accept: true}
}

// Launch records the request. With Fail set it fails at once; with Accept
// false it reports a refused request.
func (s *Sim) Launch(_ context.Context, req platform.LaunchRequest) (platform.LaunchResult, error) {
	if err := validate(req); err != nil {
		return platform.LaunchResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		if s.lastError == "" {
			s.lastError = s.fail.Error()
		}
		return platform.LaunchResult{}, s.fail
	}
	s.requests = append(s.requests, clone(req))
	return platform.LaunchResult{Accepted: s.accept}, nil
}

// SetAccept decides whether later requests are accepted.
func (s *Sim) SetAccept(accept bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accept = accept
}

// SetFail makes later requests fail with err.
func (s *Sim) SetFail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

// SetLastLaunchError is the message a degraded template reports.
func (s *Sim) SetLastLaunchError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = msg
}

// LastLaunchError implements the scaler's optional error reporter.
func (s *Sim) LastLaunchError(context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

// Requests returns a copy of every accepted request, in order.
func (s *Sim) Requests() []platform.LaunchRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]platform.LaunchRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// Asked is how many workers of a template were asked for.
func (s *Sim) Asked(template string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, req := range s.requests {
		if req.Template == template {
			n += req.Count
		}
	}
	return n
}

// Tokens is the join tokens of every request for a template, in order.
func (s *Sim) Tokens(template string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, req := range s.requests {
		if req.Template == template {
			out = append(out, req.JoinTokens...)
		}
	}
	return out
}

func clone(req platform.LaunchRequest) platform.LaunchRequest {
	req.JoinTokens = append([]string(nil), req.JoinTokens...)
	if req.Settings != nil {
		settings := make(map[string]string, len(req.Settings))
		for k, v := range req.Settings {
			settings[k] = v
		}
		req.Settings = settings
	}
	return req
}
