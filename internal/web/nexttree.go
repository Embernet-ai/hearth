package web

// The page value as a tree for the JSON mode, with the answers of the methods the templates call.
//
// encoding/json writes fields, not methods, and a template can call either: {{.Timer.Elapsed}} is a
// method on the running entry, {{.Amount}} one on a deal, {{if .Active}} one on an agent. So the JSON
// mode walks the value the way encoding/json does (same names, same tags, same handling of times and
// of anything that marshals itself) and adds one key for each method that
//
//   - some template actually references by that name (read from the parsed templates at startup, so
//     the set is exact and cannot go stale),
//   - takes no arguments and returns one plain value (a string, a number, a bool, or a time), and
//   - is not already a field.
//
// Methods that take arguments (AskedHas, IsAssigned, Query.Has) are ported in ui/src/lib/gofuncs.ts
// instead, with their golden tests. Nothing else is ever called: some methods in this codebase query
// a database, and none of them is reachable through the data a page carries.
//
// Three differences from encoding/json, all on purpose and all only in this mode. Field names are
// the Go names, not the json tag names (a template says .Query.SpaceID, the tag says space_id).
// omitempty is ignored: a template sees the Go zero value ({{if eq .Query.Kind ""}} is true for a
// kind nobody set), and JSON that left the field out would make it false. And the added methods.
// Fields tagged json:"-" stay out: that tag is the decision that a field is not sent.
//
// TestTreeMatchesEncodingJSON holds the plain walk (no extras, omitempty honored) to encoding/json
// for every page the tests render.

import (
	"embed"
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Embernet-ai/hearth/internal/platform/markdown"
)

// referenced is every name a view may read from a page that can be a method. It was taken from the Go
// templates when they were retired and lives in next/data-methods.txt; a view that needs another adds it there.
var referenced atomic.Pointer[map[string]bool]

// markdownFields are the field names a view shows as Markdown (next/data-markdown.txt): Markdown is rendered
// here, by the same renderer, and sent beside the field as Name_md, which the page prints as HTML.
var markdownFields atomic.Pointer[map[string]bool]

//go:embed next/data-methods.txt next/data-markdown.txt
var dataLists embed.FS

func init() {
	read := func(name string) map[string]bool {
		raw, err := dataLists.ReadFile(name)
		if err != nil {
			panic(err) // embedded above; cannot happen
		}
		out := map[string]bool{}
		for _, l := range strings.Split(string(raw), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				out[l] = true
			}
		}
		return out
	}
	m, md := read("next/data-methods.txt"), read("next/data-markdown.txt")
	referenced.Store(&m)
	markdownFields.Store(&md)
}

var (
	marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textType      = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func selfMarshals(t reflect.Type) bool {
	return t.Implements(marshalerType) || t.Implements(textType) ||
		(t.Kind() != reflect.Ptr && (reflect.PointerTo(t).Implements(marshalerType) || reflect.PointerTo(t).Implements(textType)))
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}

// tree converts v. extras false is the plain encoding/json shape, for the test that compares them.
func tree(v reflect.Value, extras bool) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Ptr && selfMarshals(v.Type()) {
			return raw(v)
		}
		return tree(v.Elem(), extras)
	}
	if selfMarshals(v.Type()) {
		return raw(v)
	}
	// A named string, number or bool whose methods a template calls ({{.Label}} on a Capability, which is a
	// string) cannot carry them as itself. It goes out as an object: "$" is the value, and the ported template
	// functions read "$" wherever they compare, index, print or test it.
	if extras && v.Type().PkgPath() != "" && plainResult(v.Type()) && v.Type() != timeType {
		m := map[string]any{"$": v.Interface()}
		addMethods(v, m)
		if len(m) > 1 {
			return m
		}
	}
	switch v.Kind() {
	case reflect.Struct:
		m := map[string]any{}
		structFields(v, m, extras)
		if extras {
			addMethods(v, m)
		}
		return m
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return raw(v)
		}
		fallthrough
	case reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = tree(v.Index(i), extras)
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		if k := v.Type().Key().Kind(); k != reflect.String {
			return raw(v) // integer keyed maps: let encoding/json spell the keys
		}
		m := make(map[string]any, v.Len())
		for it := v.MapRange(); it.Next(); {
			m[it.Key().String()] = tree(it.Value(), extras)
		}
		if extras {
			addMethods(v, m)
		}
		return m
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return nil
	}
	return v.Interface()
}

func raw(v reflect.Value) any {
	b, err := json.Marshal(v.Interface())
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

func structFields(v reflect.Value, into map[string]any, extras bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fv := v.Field(i)
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Ptr {
				if fv.IsNil() {
					continue
				}
				fv, ft = fv.Elem(), ft.Elem()
			}
			if ft.Kind() == reflect.Struct && !selfMarshals(ft) {
				structFields(fv, into, extras)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		// Templates read Go field names ({{.Query.SpaceID}}), so in this mode a tag's name is not used.
		if name == "" || extras {
			name = f.Name
		}
		if !extras && strings.Contains(opts, "omitempty") && isEmpty(fv) {
			continue
		}
		into[name] = tree(fv, extras)
		if extras && fv.Kind() == reflect.String {
			if md := markdownFields.Load(); md != nil && (*md)[name] {
				into[name+"_md"] = markdown.Render(fv.String(), markdown.Options{})
			}
		}
	}
}

var timeType = reflect.TypeOf(time.Time{})

func plainResult(t reflect.Type) bool {
	if t == timeType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// addMethods adds the methods of v's type that a template references, take no arguments and return one plain value,
// and have not already been written as a field.
func addMethods(v reflect.Value, into map[string]any) {
	names := referenced.Load()
	if names == nil {
		return
	}
	recv := v
	if !recv.CanAddr() {
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		recv = p.Elem()
	}
	ptr := recv.Addr()
	pt := ptr.Type()
	for i := 0; i < pt.NumMethod(); i++ {
		mt := pt.Method(i)
		n := mt.Name
		if !(*names)[n] {
			continue
		}
		if _, have := into[n]; have {
			continue
		}
		m := ptr.Method(i)
		if m.Type().NumIn() != 0 || m.Type().NumOut() != 1 || !plainResult(m.Type().Out(0)) {
			continue
		}
		into[n] = tree(m.Call(nil)[0], false)
	}
}
