package settings

import (
	"context"
	"net/url"
	"strings"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupSolver is the settings group for the Cloudflare solver
// (Settings → Indexers).
const GroupSolver = "solver"

// Solver holds the FlareSolverr-compatible solver's address (such as
// TRAWL's). Indexers that need it turn it on one by one.
type Solver struct {
	// URL is the solver's address, e.g. http://trawl:8191. Empty means
	// there's no solver; indexers that ask for one then fail with a clear
	// message.
	URL string `json:"url"`
	// TimeoutSeconds is how long one page may take to solve.
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// DefaultSolver has no solver and a 60-second timeout.
func DefaultSolver() Solver { return Solver{TimeoutSeconds: 60} }

func (s *Solver) normalize() validation.Errors {
	var errs validation.Errors
	s.URL = strings.TrimRight(strings.TrimSpace(s.URL), "/")
	if s.URL != "" {
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs.Add("url", "Enter the solver's address, like http://trawl:8191.")
		}
	}
	if s.TimeoutSeconds < 10 || s.TimeoutSeconds > 300 {
		errs.Add("timeoutSeconds", "Use 10 to 300 seconds; 60 suits most sites.")
	}
	return errs
}

// Solver returns the solver settings.
func (s *Service) Solver() Solver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.solver
}

// SaveSolver validates and stores the solver settings.
func (s *Service) SaveSolver(ctx context.Context, v Solver) (Solver, error) {
	if errs := v.normalize(); errs != nil {
		return Solver{}, errs
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupSolver, v); err != nil {
		s.mu.Unlock()
		return Solver{}, err
	}
	s.solver = v
	s.mu.Unlock()
	s.notify(GroupSolver)
	return v, nil
}
