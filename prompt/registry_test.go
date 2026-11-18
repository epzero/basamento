package prompt

import (
	"fmt"
	"sync"
	"testing"
)

func TestRegistryConcurrentUse(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ver := fmt.Sprintf("%d", n)
			tpl := &Template{
				ID: "greeting", Version: ver,
				Segments: []Segment{{Name: "main", Kind: KindStatic, Text: "hi"}},
			}
			if _, err := reg.Register(tpl); err != nil {
				t.Error(err)
				return
			}
			if _, err := reg.Resolve("greeting@" + ver); err != nil {
				t.Error(err)
				return
			}
			if _, err := reg.Assemble("greeting@"+ver, map[string]string{}, nil); err != nil {
				// static-only template, no inputs
			}
		}(i)
	}
	wg.Wait()
}