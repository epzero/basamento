package gate

import "sort"

// Work package states, derived from the repository alone so that no tracker
// is needed to know what can be worked on.
const (
	WPDone       = "done"        // its scenarios are all active on this checkout
	WPInProgress = "in-progress" // a session holds a worktree for it
	WPReady      = "ready"       // every dependency is done
	WPBlocked    = "blocked"     // some dependency is not done
)

// WPStatus is one work package and where it stands.
type WPStatus struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	State   string   `json:"state"`
	Waiting []string `json:"waiting,omitempty"` // dependencies that are not done
	Owns    []string `json:"owns,omitempty"`
	Session string   `json:"session,omitempty"` // identity of the latest session working on it
	Status  string   `json:"status,omitempty"`  // what that session left behind
}

// WorkPackages reports the state of every work package in the roadmap.
//
// A work package is done when it has scenarios (@wp:<id>) and none of them is
// @planned: that is the definition of done, read from the specs.
func WorkPackages(root string) ([]WPStatus, error) {
	epics, err := LoadRoadmap(root)
	if err != nil {
		return nil, err
	}
	spec, err := Load(root)
	if err != nil {
		return nil, err
	}
	scenarios, planned := map[string]int{}, map[string]int{}
	for _, sc := range spec.Scenarios {
		scenarios[sc.WP]++
		if sc.IsPlanned {
			planned[sc.WP]++
		}
	}
	done := func(id string) bool { return scenarios[id] > 0 && planned[id] == 0 }

	working := map[string]Session{}
	// Outside a git repository there are no sessions; the roadmap still has states.
	sessions, _ := ListSessions(root, "")
	for _, s := range sessions {
		if id, err := ParseIdentity(s.Identity); err == nil && id.Role != RoleCodeReviewer && s.Issue != "" {
			working[s.Issue] = s.Session
		}
	}

	var out []WPStatus
	for _, e := range epics {
		for _, w := range e.WPs {
			st := WPStatus{ID: w.ID, Title: w.Title, Owns: w.Owns}
			for _, d := range w.Depends {
				if !done(d) {
					st.Waiting = append(st.Waiting, d)
				}
			}
			sort.Strings(st.Waiting)
			s, held := working[w.ID]
			switch {
			case done(w.ID):
				st.State, st.Waiting = WPDone, nil
			case held:
				st.State, st.Session, st.Status = WPInProgress, s.Identity, s.Status
			case len(st.Waiting) == 0:
				st.State = WPReady
			default:
				st.State = WPBlocked
			}
			out = append(out, st)
		}
	}
	return out, nil
}
