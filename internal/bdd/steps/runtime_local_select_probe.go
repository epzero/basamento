package steps

import (
	"fmt"
	"sync"
)

// selectProbe is a harness future passed last to dc.Select. local.Select polls
// every future's Ready() in index order before it subscribes; the probe is
// never ready, so once its Ready() runs the at-call phase is over and later
// timer completions are ordered by firstCompleted, not the lowest-index tie rule.
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
