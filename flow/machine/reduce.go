package machine

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/epzero/basamento/flow"
)

// mergeStep applies one step's update to state. A set node's update is an
// object of key assignments; any other node writes its output to its Out key.
// The key's reducer decides how (ADR-0003).
func (m *Machine) mergeStep(state map[string]any, node flow.Node, update json.RawMessage) error {
	if node.Kind == "set" {
		if len(update) == 0 {
			return nil
		}
		decoded, err := decodeJSON(update)
		if err != nil {
			return invalid("flow: set update: "+err.Error(), nil)
		}
		assignments, ok := decoded.(map[string]any)
		if !ok {
			return invalid("flow: set update is not an object", nil)
		}
		for _, key := range sortedKeys(assignments) {
			if err := m.reduceKey(state, key, assignments[key]); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Out == "" {
		return nil
	}
	var value any
	if len(update) > 0 {
		decoded, err := decodeJSON(update)
		if err != nil {
			return invalid(fmt.Sprintf("flow: node output for %q: %s", node.Out, err.Error()), map[string]any{"key": node.Out})
		}
		value = decoded
	}
	return m.reduceKey(state, node.Out, value)
}

func (m *Machine) reduceKey(state map[string]any, key string, update any) error {
	reducer := m.def.State.Reducers[key]
	if reducer == "" {
		reducer = "replace"
	}
	current := state[key]
	var merged any
	var err error
	switch {
	case reducer == "replace":
		merged = update
	case reducer == "append":
		merged, err = reduceAppend(current, update)
	case reducer == "merge":
		merged, err = reduceMerge(current, update)
	case reducer == "sum":
		merged, err = reduceSum(current, update)
	case strings.HasPrefix(reducer, "merge_by(") && strings.HasSuffix(reducer, ")"):
		field := reducer[len("merge_by(") : len(reducer)-1]
		merged, err = reduceMergeBy(current, update, field)
	default:
		err = fmt.Errorf("unknown reducer %q", reducer)
	}
	if err != nil {
		return invalid(fmt.Sprintf("flow: state key %q: %s", key, err.Error()), map[string]any{"key": key, "reducer": reducer})
	}
	state[key] = merged
	return nil
}

// reduceAppend appends the update to the current array. An array update
// contributes its elements; any other value is one element.
func reduceAppend(current, update any) (any, error) {
	list := []any{}
	if current != nil {
		existing, ok := current.([]any)
		if !ok {
			return nil, fmt.Errorf("append needs an array, have %T", current)
		}
		list = append(list, existing...)
	}
	if added, ok := update.([]any); ok {
		list = append(list, added...)
	} else {
		list = append(list, update)
	}
	return list, nil
}

// reduceMerge shallow-merges objects; the update wins on a shared key.
func reduceMerge(current, update any) (any, error) {
	merged := map[string]any{}
	if current != nil {
		existing, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("merge needs an object, have %T", current)
		}
		for key, value := range existing {
			merged[key] = value
		}
	}
	added, ok := update.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("merge needs an object update, have %T", update)
	}
	for key, value := range added {
		merged[key] = value
	}
	return merged, nil
}

// reduceMergeBy merges arrays of objects keyed by field. An object replaces
// the entry with the same key at its first position; a new key is appended.
func reduceMergeBy(current, update any, field string) (any, error) {
	list, index, err := indexByField(current, field)
	if err != nil {
		return nil, err
	}
	added, _, err := indexByField(update, field)
	if err != nil {
		return nil, err
	}
	for _, item := range added {
		key, err := mergeKey(item, field)
		if err != nil {
			return nil, err
		}
		if at, ok := index[key]; ok {
			list[at] = item
			continue
		}
		index[key] = len(list)
		list = append(list, item)
	}
	return list, nil
}

func indexByField(value any, field string) ([]any, map[string]int, error) {
	list := []any{}
	index := map[string]int{}
	if value == nil {
		return list, index, nil
	}
	existing, ok := value.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("merge_by(%s) needs an array, have %T", field, value)
	}
	for _, item := range existing {
		key, err := mergeKey(item, field)
		if err != nil {
			return nil, nil, err
		}
		if at, ok := index[key]; ok {
			list[at] = item
			continue
		}
		index[key] = len(list)
		list = append(list, item)
	}
	return list, index, nil
}

func mergeKey(item any, field string) (string, error) {
	object, ok := item.(map[string]any)
	if !ok {
		return "", fmt.Errorf("merge_by(%s) needs objects, have %T", field, item)
	}
	value, ok := object[field]
	if !ok {
		return "", fmt.Errorf("merge_by(%s): an entry has no %q", field, field)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("merge_by(%s): %s", field, err.Error())
	}
	return string(encoded), nil
}

func reduceSum(current, update any) (any, error) {
	if current == nil {
		if _, _, err := numberValue(update); err != nil {
			return nil, err
		}
		return update, nil
	}
	return addNumbers(current, update)
}

func addNumbers(a, b any) (any, error) {
	left, leftInt, err := numberValue(a)
	if err != nil {
		return nil, err
	}
	right, rightInt, err := numberValue(b)
	if err != nil {
		return nil, err
	}
	if leftInt && rightInt {
		sum, err := strconv.ParseInt(left, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sum: %s", err.Error())
		}
		add, err := strconv.ParseInt(right, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sum: %s", err.Error())
		}
		if add > 0 && sum > math.MaxInt64-add {
			return nil, fmt.Errorf("overflow")
		}
		if add < 0 && sum < math.MinInt64-add {
			return nil, fmt.Errorf("overflow")
		}
		return json.Number(strconv.FormatInt(sum+add, 10)), nil
	}
	sum, err := strconv.ParseFloat(left, 64)
	if err != nil {
		return nil, fmt.Errorf("sum: %s", err.Error())
	}
	add, err := strconv.ParseFloat(right, 64)
	if err != nil {
		return nil, fmt.Errorf("sum: %s", err.Error())
	}
	result := sum + add
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return nil, fmt.Errorf("overflow")
	}
	return json.Number(strconv.FormatFloat(result, 'g', -1, 64)), nil
}

func numberValue(value any) (text string, integral bool, err error) {
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case float64:
		text = strconv.FormatFloat(typed, 'g', -1, 64)
	case int:
		return strconv.Itoa(typed), true, nil
	case int64:
		return strconv.FormatInt(typed, 10), true, nil
	default:
		return "", false, fmt.Errorf("sum needs numbers, have %T", value)
	}
	if strings.ContainsAny(text, ".eE") {
		return text, false, nil
	}
	return text, true, nil
}
