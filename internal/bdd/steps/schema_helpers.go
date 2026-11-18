package steps

import (
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/epzero/basamento/core/schema"
)

var valueFieldRE = regexp.MustCompile(`(\w+)\s+"([^"]*)"`)

func tableToStructFields(table *godog.Table, named map[string]reflect.Type) ([]reflect.StructField, error) {
	if len(table.Rows) < 2 {
		return nil, fmt.Errorf("empty field table")
	}
	nameIdx, typeIdx, jsonIdx, schemaIdx := colIndex(table)
	var fields []reflect.StructField
	for _, row := range table.Rows[1:] {
		name := cellAt(row, nameIdx)
		goType, err := parseGoType(cellAt(row, typeIdx), named)
		if err != nil {
			return nil, err
		}
		tag := cellAt(row, jsonIdx)
		var structTag reflect.StructTag
		switch tag {
		case "-":
			structTag = reflect.StructTag(`json:"-"`)
		case "":
			structTag = reflect.StructTag(fmt.Sprintf(`json:"%s"`, name))
		default:
			structTag = reflect.StructTag(fmt.Sprintf(`json:"%s"`, tag))
		}
		if schemaIdx >= 0 {
			if jsTag := cellAt(row, schemaIdx); jsTag != "" {
				structTag += reflect.StructTag(fmt.Sprintf(` jsonschema:"%s"`, jsTag))
			}
		}
		fields = append(fields, reflect.StructField{
			Name: name,
			Type: goType,
			Tag:  structTag,
		})
	}
	return fields, nil
}

func colIndex(header *godog.Table) (field, typ, json, jsonschema int) {
	if len(header.Rows) == 0 {
		return 0, 1, 2, -1
	}
	row := header.Rows[0]
	field, typ, json, jsonschema = 0, 1, 2, -1
	for i, c := range row.Cells {
		switch strings.TrimSpace(c.Value) {
		case "field":
			field = i
		case "type", "go type":
			typ = i
		case "json tag":
			json = i
		case "jsonschema tag":
			jsonschema = i
		}
	}
	return
}

func cellAt(row *messages.PickleTableRow, idx int) string {
	if idx < 0 || idx >= len(row.Cells) {
		return ""
	}
	return strings.TrimSpace(row.Cells[idx].Value)
}

func parseGoType(s string, named map[string]reflect.Type) (reflect.Type, error) {
	s = strings.TrimSpace(s)
	if t, ok := named[s]; ok {
		return t, nil
	}
	if strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "<") {
		return nil, fmt.Errorf("unknown type %q", s)
	}
	switch s {
	case "string":
		return reflect.TypeFor[string](), nil
	case "int":
		return reflect.TypeFor[int](), nil
	case "int64":
		return reflect.TypeFor[int64](), nil
	case "float64":
		return reflect.TypeFor[float64](), nil
	case "bool":
		return reflect.TypeFor[bool](), nil
	case "chan int":
		return reflect.TypeFor[chan int](), nil
	case "func()":
		return reflect.TypeFor[func()](), nil
	case "complex128":
		return reflect.TypeFor[complex128](), nil
	}
	if strings.HasPrefix(s, "[]") {
		elem, err := parseGoType(strings.TrimPrefix(s, "[]"), named)
		if err != nil {
			return nil, err
		}
		return reflect.SliceOf(elem), nil
	}
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return nil, fmt.Errorf("bad array type %q", s)
		}
		n, err := strconv.Atoi(s[1:end])
		if err != nil {
			return nil, err
		}
		elem, err := parseGoType(s[end+1:], named)
		if err != nil {
			return nil, err
		}
		return reflect.ArrayOf(n, elem), nil
	}
	if strings.HasPrefix(s, "map[string]") {
		elem, err := parseGoType(strings.TrimPrefix(s, "map[string]"), named)
		if err != nil {
			return nil, err
		}
		return reflect.MapOf(reflect.TypeFor[string](), elem), nil
	}
	return nil, fmt.Errorf("unknown type %q", s)
}

func parseQuotedList(s string) []string {
	var out []string
	for {
		i := strings.Index(s, `"`)
		if i < 0 {
			break
		}
		rest := s[i+1:]
		j := strings.Index(rest, `"`)
		if j < 0 {
			break
		}
		out = append(out, rest[:j])
		s = rest[j+1:]
	}
	return out
}

func keysOf(m map[string]any) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func firstGenKey(w *schemaWorld) string {
	for k := range w.generated {
		return k
	}
	return ""
}

func splitCSVOrder(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	parts := strings.Split(s, `", "`)
	for i := range parts {
		parts[i] = strings.Trim(parts[i], `"`)
	}
	return parts
}

func memberNamesInOrder(raw []byte) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not object")
	}
	var names []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("key type")
		}
		names = append(names, name)
		var skip any
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// compareViolationRuns checks that exactly three validation runs match want.
func compareViolationRuns(runs [][]schema.Violation, want []schema.Violation) error {
	if len(runs) != 3 {
		return fmt.Errorf("expected 3 validation runs, got %d", len(runs))
	}
	for ri, got := range runs {
		if len(got) != len(want) {
			return fmt.Errorf("run %d: got %+v", ri, got)
		}
		for i := range want {
			if got[i].Location != want[i].Location || got[i].Keyword != want[i].Keyword {
				return fmt.Errorf("run %d index %d: %+v", ri, i, got[i])
			}
		}
	}
	return nil
}

func tableViolations(table *godog.Table) []schema.Violation {
	var out []schema.Violation
	for i, row := range table.Rows {
		if i == 0 {
			continue
		}
		out = append(out, schema.Violation{
			Location: strings.TrimSpace(row.Cells[0].Value),
			Keyword:  strings.TrimSpace(row.Cells[1].Value),
		})
	}
	return out
}

func setFieldFromSpec(f reflect.Value, spec string) error {
	spec = strings.TrimSpace(spec)
	switch f.Kind() {
	case reflect.String:
		spec = strings.Trim(spec, `"`)
		f.SetString(spec)
	case reflect.Int, reflect.Int64:
		spec = strings.Trim(spec, `"`)
		n, err := strconv.Atoi(spec)
		if err != nil {
			return err
		}
		f.SetInt(int64(n))
	case reflect.Slice:
		if spec == `null` {
			f.Set(reflect.Zero(f.Type()))
			return nil
		}
		var items []string
		if strings.Contains(spec, " and ") {
			for _, part := range strings.Split(spec, " and ") {
				part = strings.TrimSpace(part)
				part = strings.Trim(part, `"`)
				items = append(items, part)
			}
		} else {
			for _, m := range valueFieldRE.FindAllStringSubmatch(spec+",", -1) {
				items = append(items, m[2])
			}
			if len(items) == 0 {
				items = parseQuotedList(spec)
			}
		}
		sl := reflect.MakeSlice(f.Type(), len(items), len(items))
		for i, it := range items {
			sl.Index(i).SetString(it)
		}
		f.Set(sl)
	case reflect.Map:
		if spec == `null` {
			f.Set(reflect.Zero(f.Type()))
			return nil
		}
		m := reflect.MakeMap(f.Type())
		kv := strings.SplitN(spec, "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			n, err := strconv.Atoi(strings.TrimSpace(kv[1]))
			if err != nil {
				return err
			}
			m.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(n).Convert(f.Type().Elem()))
		}
		f.Set(m)
	default:
		return fmt.Errorf("unsupported field kind %s", f.Kind())
	}
	return nil
}

func parseSchemaObject(raw json.RawMessage) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	return m, nil
}

func rejectFromErr(err error, loc, kw string) error {
	vs := schema.Violations(err)
	for _, v := range vs {
		if v.Location == loc && v.Keyword == kw {
			return nil
		}
	}
	return fmt.Errorf("violations %+v", vs)
}

func compileGenerated(w *schemaWorld, alias string) (*schema.Compiled, error) {
	sch := w.generated[alias]
	if sch == nil {
		sch = w.generated[firstGenKey(w)]
	}
	if sch == nil {
		return nil, fmt.Errorf("no generated schema")
	}
	return schema.Compile(sch.Raw)
}

// loopbackHTTPProbe listens on loopback TCP and counts inbound connections.
// It stands in for an HTTP server without importing net/http.
type loopbackHTTPProbe struct {
	ln       net.Listener
	baseURL  string
	conns    atomic.Int32
	stopOnce sync.Once
	done     chan struct{}
}

func newLoopbackHTTPProbe() (*loopbackHTTPProbe, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, "", fmt.Errorf("loopback listener: not TCP")
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", tcpAddr.Port)
	p := &loopbackHTTPProbe{ln: ln, baseURL: baseURL, done: make(chan struct{})}
	go p.acceptLoop()
	return p, baseURL, nil
}

func (p *loopbackHTTPProbe) acceptLoop() {
	defer close(p.done)
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.conns.Add(1)
		_ = c.Close()
	}
}

// shutdown closes the listener, waits for the accept loop to exit, and
// returns how many connections were accepted.
func (p *loopbackHTTPProbe) shutdown() int {
	p.stopOnce.Do(func() { _ = p.ln.Close() })
	<-p.done
	return int(p.conns.Load())
}
