package expr

import (
	"errors"
	"reflect"
)

const maxJSONWalkDepth = 10000

var errJSONDepthExceeded = errors.New("json depth exceeded")

type jsonWalk struct {
	depth int
	seen  map[uintptr]struct{}
}

func newJSONWalk() *jsonWalk {
	return &jsonWalk{seen: make(map[uintptr]struct{})}
}

func addrOf(v any) uintptr {
	if v == nil {
		return 0
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer:
		return rv.Pointer()
	default:
		return 0
	}
}

func (w *jsonWalk) down(ptr uintptr) error {
	w.depth++
	if w.depth > maxJSONWalkDepth {
		return errJSONDepthExceeded
	}
	if ptr != 0 {
		if _, ok := w.seen[ptr]; ok {
			return errJSONDepthExceeded
		}
		w.seen[ptr] = struct{}{}
	}
	return nil
}

func (w *jsonWalk) up(ptr uintptr) {
	w.depth--
	if ptr != 0 {
		delete(w.seen, ptr)
	}
}

func isJSONDepthErr(err error) bool {
	return errors.Is(err, errJSONDepthExceeded)
}
