package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
)

type schemaWorld struct {
	texts      map[string][]byte
	canonical  map[string][]byte
	schemas    map[string]json.RawMessage
	compiled   *schema.Compiled
	set        *schema.Set
	types      map[string]reflect.Type
	generated  map[string]*schema.Schema
	lastErr    error
	lastViol   []schema.Violation
	httpServer *httptest.Server
	httpReqs   int
	fileURI    string
	expectURL  string
}

func newSchemaWorld() *schemaWorld {
	return &schemaWorld{
		texts:     map[string][]byte{},
		canonical: map[string][]byte{},
		schemas:   map[string]json.RawMessage{},
		types:     map[string]reflect.Type{},
		generated: map[string]*schema.Schema{},
	}
}

func schemaCtx(ctx context.Context) *schemaWorld {
	return world(ctx).Schema
}

func registerSchema(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w := world(ctx)
		if w.Schema == nil {
			w.Schema = newSchemaWorld()
		}
		return ctx, nil
	})

	sc.Step(`^the JSON text:$`, func(ctx context.Context, doc *godog.DocString) error {
		schemaCtx(ctx).texts[""] = []byte(strings.TrimSpace(doc.Content))
		return nil
	})
	sc.Step(`^the JSON text "([^"]*)":$`, func(ctx context.Context, name string, doc *godog.DocString) error {
		schemaCtx(ctx).texts[name] = []byte(strings.TrimSpace(doc.Content))
		return nil
	})
	sc.Step(`^the JSON text is canonicalized$`, func(ctx context.Context) error {
		w := schemaCtx(ctx)
		out, err := schema.Canonicalize(w.texts[""])
		w.lastErr = err
		w.canonical[""] = out
		return nil
	})
	sc.Step(`^the JSON text (\{[\s\S]*\}|[-0-9.eE].*)$`, func(ctx context.Context, text string) error {
		schemaCtx(ctx).texts[""] = []byte(strings.TrimSpace(text))
		return nil
	})
	sc.Step(`^the canonical form is:$`, func(ctx context.Context, doc *godog.DocString) error {
		got := string(schemaCtx(ctx).canonical[""])
		want := strings.TrimSpace(doc.Content)
		if got != want {
			return fmt.Errorf("got %q want %q", got, want)
		}
		return nil
	})
	sc.Step(`^the canonical form is (.+)$`, func(ctx context.Context, want string) error {
		got := string(schemaCtx(ctx).canonical[""])
		if got != strings.TrimSpace(want) {
			return fmt.Errorf("got %q want %q", got, want)
		}
		return nil
	})
	sc.Step(`^the member names of the canonical form are in the order (.+)$`, func(ctx context.Context, order string) error {
		keys, err := memberNamesInOrder(schemaCtx(ctx).canonical[""])
		if err != nil {
			return err
		}
		want := splitCSVOrder(order)
		if !reflect.DeepEqual(keys, want) {
			return fmt.Errorf("order %v want %v", keys, want)
		}
		return nil
	})
	sc.Step(`^both JSON texts are canonicalized$`, func(ctx context.Context) error {
		w := schemaCtx(ctx)
		for _, name := range []string{"first", "second"} {
			out, err := schema.Canonicalize(w.texts[name])
			if err != nil {
				return err
			}
			w.canonical[name] = out
		}
		return nil
	})
	sc.Step(`^the canonical forms of "([^"]*)" and "([^"]*)" are identical$`, func(ctx context.Context, a, b string) error {
		w := schemaCtx(ctx)
		if string(w.canonical[a]) != string(w.canonical[b]) {
			return fmt.Errorf("differ")
		}
		return nil
	})
	sc.Step(`^its canonical form is canonicalized again$`, func(ctx context.Context) error {
		w := schemaCtx(ctx)
		out, err := schema.Canonicalize(w.canonical[""])
		if err != nil {
			return err
		}
		w.canonical["second"] = out
		return nil
	})
	sc.Step(`^the second canonical form is identical to the first$`, func(ctx context.Context) error {
		w := schemaCtx(ctx)
		if string(w.canonical["second"]) != string(w.canonical[""]) {
			return fmt.Errorf("changed")
		}
		return nil
	})
	sc.Step(`^the canonicalization fails with code "([^"]*)" at instance location "([^"]*)"$`, func(ctx context.Context, code, loc string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil {
			return fmt.Errorf("succeeded")
		}
		if string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("code %s", core.CodeOf(w.lastErr))
		}
		if schema.ErrLocation(w.lastErr) != loc {
			return fmt.Errorf("location %q", schema.ErrLocation(w.lastErr))
		}
		return nil
	})

	sc.Step(`^a Go struct type "([^"]*)" with fields:$`, func(ctx context.Context, typeName string, table *godog.Table) error {
		fields, err := tableToStructFields(table)
		if err != nil {
			return err
		}
		t := reflect.StructOf(fields)
		schemaCtx(ctx).types[typeName] = t
		registerNamedType(typeName, t)
		return nil
	})

	sc.Step(`^a schema is generated from "([^"]*)"$`, func(ctx context.Context, name string) error {
		return generateSchema(ctx, name, name)
	})
	sc.Step(`^a schema is generated from "([^"]*)" as "([^"]*)"$`, func(ctx context.Context, name, alias string) error {
		return generateSchema(ctx, name, alias)
	})

	sc.Step(`^the schema has exactly the properties (.+)$`, assertProperties)
	sc.Step(`^the schema requires exactly the properties (.+)$`, assertRequired)
	sc.Step(`^property "([^"]*)" of the schema has the description "([^"]*)"$`, assertDescription)

	sc.Step(`^the schema accepts the instance (\{[^}].+|\{.+\}|\[.+\])$`, func(ctx context.Context, inst string) error {
		w := schemaCtx(ctx)
		if w.compiled != nil {
			return w.compiled.Validate(json.RawMessage(inst))
		}
		c, err := compileGenerated(w, firstGenKey(w))
		if err != nil {
			return err
		}
		return c.Validate(json.RawMessage(inst))
	})
	sc.Step(`^the schema rejects the instance (.+) with a violation at "([^"]*)" of keyword "([^"]*)"$`, func(ctx context.Context, inst, loc, kw string) error {
		w := schemaCtx(ctx)
		var err error
		if w.compiled != nil {
			err = w.compiled.Validate(json.RawMessage(inst))
		} else {
			c, cErr := compileGenerated(w, firstGenKey(w))
			if cErr != nil {
				return cErr
			}
			err = c.Validate(json.RawMessage(inst))
		}
		if err == nil {
			return fmt.Errorf("accepted")
		}
		return rejectFromErr(err, loc, kw)
	})
	sc.Step(`^the schema (accepts|rejects) the instance \{\}$`, func(ctx context.Context, verdict string) error {
		if verdict == "accepts" {
			w := schemaCtx(ctx)
			c, err := compileGenerated(w, firstGenKey(w))
			if err != nil {
				return err
			}
			return c.Validate(json.RawMessage(`{}`))
		}
		w := schemaCtx(ctx)
		c, err := compileGenerated(w, firstGenKey(w))
		if err != nil {
			return err
		}
		err = c.Validate(json.RawMessage(`{}`))
		if err == nil {
			return fmt.Errorf("accepted")
		}
		vs := schema.Violations(err)
		if len(vs) == 0 {
			return fmt.Errorf("no violations")
		}
		return nil
	})

	sc.Step(`^the generation fails with code "([^"]*)" naming the field path "([^"]*)"$`, func(ctx context.Context, code, path string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil {
			return fmt.Errorf("succeeded")
		}
		if string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("code")
		}
		var pe *core.Error
		if errors.As(w.lastErr, &pe) && pe.Details != nil {
			if fp, ok := pe.Details["field"].(string); ok && fp == path {
				return nil
			}
		}
		return fmt.Errorf("field path mismatch")
	})
	sc.Step(`^the canonical forms of schemas "([^"]*)" and "([^"]*)" are identical$`, func(ctx context.Context, a, b string) error {
		w := schemaCtx(ctx)
		if string(w.generated[a].Raw) != string(w.generated[b].Raw) {
			return fmt.Errorf("differ")
		}
		return nil
	})

	sc.Step(`^a value of "([^"]*)" that is (.+)$`, func(ctx context.Context, typeName, spec string) error {
		t := schemaCtx(ctx).types[typeName]
		v := reflect.New(t).Elem()
		spec = strings.TrimSpace(spec)
		if spec == "the zero value" {
			schemaCtx(ctx).texts["struct_value"] = mustJSON(v.Interface())
			return nil
		}
		for _, part := range splitValueSpec(spec) {
			if err := applyValueSpecByJSONName(v, part); err != nil {
				return err
			}
		}
		schemaCtx(ctx).texts["struct_value"] = mustJSON(v.Interface())
		return nil
	})
	sc.Step(`^the value is encoded with encoding/json$`, func(context.Context) error { return nil })
	sc.Step(`^the schema generated from "([^"]*)" accepts the encoded value$`, func(ctx context.Context, typeName string) error {
		sch, err := schema.ForType(schemaCtx(ctx).types[typeName])
		if err != nil {
			return err
		}
		c, err := schema.Compile(sch.Raw)
		if err != nil {
			return err
		}
		return c.Validate(schemaCtx(ctx).texts["struct_value"])
	})

	sc.Step(`^the schema:$`, func(ctx context.Context, doc *godog.DocString) error {
		schemaCtx(ctx).schemas[""] = json.RawMessage(strings.TrimSpace(doc.Content))
		return nil
	})
	sc.Step(`^the schema is compiled$`, func(ctx context.Context) error {
		w := schemaCtx(ctx)
		c, err := schema.Compile(w.schemas[""])
		w.lastErr = err
		w.compiled = c
		return nil
	})
	sc.Step(`^the compilation fails with code "([^"]*)" at schema location "([^"]*)"$`, func(ctx context.Context, code, loc string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil || string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("err")
		}
		for _, v := range schema.SchemaViolations(w.lastErr) {
			if v.SchemaLocation == loc {
				return nil
			}
		}
		return fmt.Errorf("violations %+v", schema.SchemaViolations(w.lastErr))
	})

	sc.Step(`^an HTTP server outside the schema set serving "([^"]*)":$`, func(ctx context.Context, name string, doc *godog.DocString) error {
		w := schemaCtx(ctx)
		w.httpReqs = 0
		w.httpServer = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			w.httpReqs++
			_, _ = rw.Write([]byte(strings.TrimSpace(doc.Content)))
		}))
		w.expectURL = w.httpServer.URL + "/" + name
		_ = name
		return nil
	})
	sc.Step(`^a schema referencing that server's URL for "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := schemaCtx(ctx)
		ref := w.httpServer.URL + "/" + name
		w.schemas[""] = json.RawMessage(fmt.Sprintf(`{"$ref": %q}`, ref))
		return nil
	})
	sc.Step(`^the compilation fails with code "([^"]*)" naming that URL$`, func(ctx context.Context, code string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil || string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("err")
		}
		if schema.ReferenceFromErr(w.lastErr) != w.expectURL {
			return fmt.Errorf("ref %q", schema.ReferenceFromErr(w.lastErr))
		}
		return nil
	})
	sc.Step(`^the HTTP server received 0 requests$`, func(ctx context.Context) error {
		if schemaCtx(ctx).httpReqs != 0 {
			return fmt.Errorf("requests %d", schemaCtx(ctx).httpReqs)
		}
		return nil
	})

	sc.Step(`^a schema file "([^"]*)" on disk containing:$`, func(ctx context.Context, name string, doc *godog.DocString) error {
		dir := filepath.Join(os.TempDir(), "basamento-schema-bdd")
		_ = os.MkdirAll(dir, 0o755)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.TrimSpace(doc.Content)), 0o644); err != nil {
			return err
		}
		schemaCtx(ctx).fileURI = "file://" + filepath.ToSlash(path)
		return nil
	})
	sc.Step(`^a schema referencing that file's URI$`, func(ctx context.Context) error {
		ref := schemaCtx(ctx).fileURI
		schemaCtx(ctx).schemas[""] = json.RawMessage(fmt.Sprintf(`{"$ref": %q}`, ref))
		return nil
	})
	sc.Step(`^the compilation fails with code "([^"]*)" naming that URI$`, func(ctx context.Context, code string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil || string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("err")
		}
		got := schema.ReferenceFromErr(w.lastErr)
		if got != w.fileURI && !strings.HasSuffix(got, strings.TrimPrefix(w.fileURI, "file://")) {
			return fmt.Errorf("ref %q", got)
		}
		return nil
	})
	sc.Step(`^the compilation fails with code "([^"]*)" naming the reference "([^"]*)"$`, func(ctx context.Context, code, ref string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil || string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("err")
		}
		if schema.ReferenceFromErr(w.lastErr) != ref {
			return fmt.Errorf("ref %q", schema.ReferenceFromErr(w.lastErr))
		}
		return nil
	})

	sc.Step(`^a schema set with the schema "([^"]*)":$`, func(ctx context.Context, id string, doc *godog.DocString) error {
		w := schemaCtx(ctx)
		if w.set == nil {
			w.set = schema.NewSet()
		}
		return w.set.Add(id, json.RawMessage(strings.TrimSpace(doc.Content)))
	})
	sc.Step(`^the schema "([^"]*)" in the same schema set:$`, func(ctx context.Context, id string, doc *godog.DocString) error {
		return schemaCtx(ctx).set.Add(id, json.RawMessage(strings.TrimSpace(doc.Content)))
	})
	sc.Step(`^schema "([^"]*)" of the schema set is compiled$`, func(ctx context.Context, id string) error {
		w := schemaCtx(ctx)
		c, err := w.set.Compile(id)
		w.lastErr = err
		w.compiled = c
		return nil
	})

	sc.Step(`^the instance (.+) is validated$`, validateInstance)
	sc.Step(`^the instance (.+) is validated 3 times$`, func(ctx context.Context, inst string) error {
		for range 3 {
			if err := validateInstance(ctx, inst); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the validation fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if schemaCtx(ctx).lastErr == nil || string(core.CodeOf(schemaCtx(ctx).lastErr)) != code {
			return fmt.Errorf("err")
		}
		return nil
	})
	sc.Step(`^the only violation is at "([^"]*)" of keyword "([^"]*)"$`, func(ctx context.Context, loc, kw string) error {
		vs := schemaCtx(ctx).lastViol
		if len(vs) != 1 || vs[0].Location != loc || vs[0].Keyword != kw {
			return fmt.Errorf("violations %+v", vs)
		}
		return nil
	})
	sc.Step(`^each validation lists exactly these violations, in this order:$`, func(ctx context.Context, table *godog.Table) error {
		want := tableViolations(table)
		got := schemaCtx(ctx).lastViol
		if len(got) != len(want) {
			return fmt.Errorf("got %+v", got)
		}
		for i := range want {
			if got[i].Location != want[i].Location || got[i].Keyword != want[i].Keyword {
				return fmt.Errorf("index %d: %+v", i, got[i])
			}
		}
		return nil
	})
	sc.Step(`^the instance is (accepted|rejected)$`, func(ctx context.Context, verdict string) error {
		err := schemaCtx(ctx).lastErr
		if verdict == "accepted" && err != nil {
			return err
		}
		if verdict == "rejected" && err == nil {
			return fmt.Errorf("accepted")
		}
		return nil
	})
	sc.Step(`^the validation fails with code "([^"]*)" at instance location "([^"]*)"$`, func(ctx context.Context, code, loc string) error {
		w := schemaCtx(ctx)
		if w.lastErr == nil || string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("err")
		}
		if el := schema.ErrLocation(w.lastErr); el != "" {
			if el == loc {
				return nil
			}
		}
		vs := schema.Violations(w.lastErr)
		if len(vs) == 1 && vs[0].Location == loc {
			return nil
		}
		return fmt.Errorf("loc %+v errloc %q", vs, schema.ErrLocation(w.lastErr))
	})
}

func generateSchema(ctx context.Context, typeName, alias string) error {
	w := schemaCtx(ctx)
	t := w.types[typeName]
	sch, err := schema.ForType(t)
	if err != nil {
		w.lastErr = err
		return nil
	}
	w.generated[alias] = sch
	w.lastErr = nil
	return nil
}

func validateInstance(ctx context.Context, inst string) error {
	w := schemaCtx(ctx)
	if w.compiled == nil {
		var err error
		w.compiled, err = schema.Compile(w.schemas[""])
		if err != nil {
			return err
		}
	}
	w.lastErr = w.compiled.Validate(json.RawMessage(inst))
	w.lastViol = schema.Violations(w.lastErr)
	return nil
}

func assertProperties(ctx context.Context, list string) error {
	sch := schemaCtx(ctx).generated[firstGenKey(schemaCtx(ctx))]
	m, err := schema.ParseSchemaObject(sch.Raw)
	if err != nil {
		return err
	}
	props, _ := m["properties"].(map[string]any)
	want := parseQuotedList(list)
	got := keysOf(props)
	sortStrings(&got)
	sortStrings(&want)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("properties %v want %v", got, want)
	}
	return nil
}

func assertRequired(ctx context.Context, list string) error {
	sch := schemaCtx(ctx).generated[firstGenKey(schemaCtx(ctx))]
	m, err := schema.ParseSchemaObject(sch.Raw)
	if err != nil {
		return err
	}
	req, _ := m["required"].([]any)
	want := parseQuotedList(list)
	got := make([]string, 0, len(req))
	for _, r := range req {
		got = append(got, fmt.Sprint(r))
	}
	sortStrings(&got)
	sortStrings(&want)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("required %v want %v", got, want)
	}
	return nil
}

func assertDescription(ctx context.Context, prop, desc string) error {
	sch := schemaCtx(ctx).generated[firstGenKey(schemaCtx(ctx))]
	m, err := schema.ParseSchemaObject(sch.Raw)
	if err != nil {
		return err
	}
	props, _ := m["properties"].(map[string]any)
	p, _ := props[prop].(map[string]any)
	if p == nil || p["description"] != desc {
		return fmt.Errorf("description %v", p)
	}
	return nil
}

func sortStrings(s *[]string) {
	sort := *s
	for i := 0; i < len(sort); i++ {
		for j := i + 1; j < len(sort); j++ {
			if sort[j] < sort[i] {
				sort[i], sort[j] = sort[j], sort[i]
			}
		}
	}
	*s = sort
}

func splitValueSpec(spec string) []string {
	var parts []string
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func applyValueSpecByJSONName(v reflect.Value, part string) error {
	parts := strings.SplitN(strings.TrimSpace(part), " ", 2)
	if len(parts) != 2 {
		return fmt.Errorf("bad part %q", part)
	}
	jsonName := parts[0]
	valSpec := strings.TrimSpace(parts[1])
	f, err := fieldByJSONName(v, jsonName)
	if err != nil {
		return err
	}
	return setFieldFromSpec(f, valSpec)
}

func fieldByJSONName(v reflect.Value, jsonName string) (reflect.Value, error) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := sf.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = sf.Name
		}
		if name == jsonName {
			return v.Field(i), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("no field json:%s", jsonName)
}
