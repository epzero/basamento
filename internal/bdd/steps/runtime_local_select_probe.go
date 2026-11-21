package steps

import (
	"fmt"
	"sync"
)

// selectProbe is a harness future passed last to dc.Select. At call time Select
// observes run-owned futures under the run lock; the probe is never ready, so
// once its Ready() runs the at-call snapshot is done and later completions
// compete by completion seq via firstCompleted.
type selectProbe struct {
	once  sync.Once
	began func()
}

func (p *selectProbe) Ready() bool {
	if p != nil && p.began != nil {
		p.once.Do(p.began)
	}
	return false
}

func (p *selectProbe) Get(_ any) error {
	return fmt.Errorf("bdd: select probe must not complete")
}
