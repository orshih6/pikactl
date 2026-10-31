package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Schema is the part of JSON Schema the catalog's input schemas use. Anything
// else in a schema is ignored rather than rejected, so that Pika adding a
// keyword cannot stop an operation from running: the API validates the request
// in the end, and the quote checks it before any money moves.
type Schema struct {
	Type        string
	Title       string
	Description string
	Enum        []any
	Const       any
	Default     any
	AnyOf       []*Schema
	OneOf       []*Schema
	AllOf       []*Schema
	Items       *Schema
	Properties  map[string]*Schema
	Order       []string // property names as the API lists them
	Required    []string
	Ref         string
	Defs        map[string]*Schema
	MediaKinds  []string

	root *Schema
}

// UnmarshalJSON reads a schema, keeping the order of its properties.
func (s *Schema) UnmarshalJSON(data []byte) error {
	var w struct {
		Type        json.RawMessage    `json:"type"`
		Title       string             `json:"title"`
		Description string             `json:"description"`
		Enum        []any              `json:"enum"`
		Const       any                `json:"const"`
		Default     any                `json:"default"`
		AnyOf       []*Schema          `json:"anyOf"`
		OneOf       []*Schema          `json:"oneOf"`
		AllOf       []*Schema          `json:"allOf"`
		Items       json.RawMessage    `json:"items"`
		Properties  json.RawMessage    `json:"properties"`
		Required    []string           `json:"required"`
		Ref         string             `json:"$ref"`
		Defs        map[string]*Schema `json:"$defs"`
		MediaKinds  []string           `json:"media_kinds"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*s = Schema{
		Title: w.Title, Description: w.Description, Enum: w.Enum, Const: w.Const, Default: w.Default,
		AnyOf: w.AnyOf, OneOf: w.OneOf, AllOf: w.AllOf, Required: w.Required, Ref: w.Ref, Defs: w.Defs,
		MediaKinds: w.MediaKinds,
	}
	s.Type = parseType(w.Type)
	if len(w.Items) > 0 && w.Items[0] == '{' {
		s.Items = &Schema{}
		if err := json.Unmarshal(w.Items, s.Items); err != nil {
			return err
		}
	}
	if len(w.Properties) > 0 && w.Properties[0] == '{' {
		if err := json.Unmarshal(w.Properties, &s.Properties); err != nil {
			return err
		}
		s.Order = orderedKeys(w.Properties)
	}
	return nil
}

// parseType takes "string" or ["string", "null"], the two forms JSON Schema
// allows, and returns the one type that is not null.
func parseType(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, t := range many {
			if t != "null" {
				return t
			}
		}
		return "null"
	}
	return ""
}

func orderedKeys(object json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(object))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var keys []string
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return keys
		}
		key, _ := token.(string)
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// ParseSchema reads an operation's input schema. An absent one is an empty
// schema, which accepts whatever it is given.
func ParseSchema(raw json.RawMessage) (*Schema, error) {
	s := &Schema{}
	if len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, s); err != nil {
			return nil, fmt.Errorf("reading the input schema: %w", err)
		}
	}
	s.link(s)
	return s, nil
}

// link gives every nested schema the root its $refs resolve against.
func (s *Schema) link(root *Schema) {
	if s == nil {
		return
	}
	s.root = root
	for _, list := range [][]*Schema{s.AnyOf, s.OneOf, s.AllOf} {
		for _, sub := range list {
			sub.link(root)
		}
	}
	s.Items.link(root)
	for _, sub := range s.Properties {
		sub.link(root)
	}
	for _, sub := range s.Defs {
		sub.link(root)
	}
}

func (s *Schema) resolve() *Schema {
	if s == nil || s.Ref == "" {
		return s
	}
	name := strings.TrimPrefix(s.Ref, "#/$defs/")
	if s.root == nil || name == s.Ref {
		return nil
	}
	return s.root.Defs[name]
}

// branches flattens $ref, anyOf, oneOf and allOf into the concrete shapes a
// value may take, leaving out null: a flag cannot usefully send null. A schema
// can have a shape of its own beside such a list, as when an object's anyOf
// only says which of its properties are required, so both are looked at.
func (s *Schema) branches() []*Schema {
	var out []*Schema
	var walk func(*Schema, int)
	walk = func(x *Schema, depth int) {
		x = x.resolve()
		if x == nil || depth > 16 {
			return
		}
		subs := append(append(append([]*Schema(nil), x.AnyOf...), x.OneOf...), x.AllOf...)
		switch {
		case x.Type == "null":
		case x.hasShape(), len(subs) == 0 && len(x.Required) == 0:
			out = append(out, x)
		}
		for _, sub := range subs {
			walk(sub, depth+1)
		}
	}
	walk(s, 0)
	return out
}

func (s *Schema) hasShape() bool {
	return s.Type != "" || len(s.Properties) > 0 || s.Items != nil || len(s.Enum) > 0 || s.Const != nil || len(s.MediaKinds) > 0
}

// Param is one top-level input.
type Param struct {
	Name     string
	Required bool
	Schema   *Schema
}

// Params lists the inputs, required ones first, otherwise in the API's order.
func (s *Schema) Params() []Param {
	top := s.resolve()
	if top == nil {
		return nil
	}
	required := map[string]bool{}
	for _, name := range top.Required {
		required[name] = true
	}
	params := make([]Param, 0, len(top.Order))
	for _, name := range top.Order {
		params = append(params, Param{Name: name, Required: required[name], Schema: top.Properties[name]})
	}
	sort.SliceStable(params, func(i, j int) bool { return params[i].Required && !params[j].Required })
	return params
}

// TypeName is the short type a flag's help shows.
func (p Param) TypeName() string {
	branches := p.Schema.branches()
	for _, b := range branches {
		switch b.Type {
		case "array":
			if item := b.Items.branches(); len(item) == 1 && item[0].Type != "object" && item[0].Type != "array" {
				return "strings"
			}
			return "json"
		case "object":
			return "json"
		}
	}
	if len(branches) == 1 {
		switch branches[0].Type {
		case "boolean":
			return "bool"
		case "integer":
			return "int"
		case "number":
			return "float"
		}
	}
	return "string"
}

// Values lists the fixed values the parameter takes, if it has them.
func (p Param) Values() []string {
	var out []string
	for _, b := range p.Schema.branches() {
		for _, v := range b.Enum {
			out = append(out, fmt.Sprint(v))
		}
	}
	return out
}

// Default is the API's stated default as text, or "" when it has none.
func (p Param) Default() string {
	s := p.Schema.resolve()
	if s == nil || s.Default == nil {
		return ""
	}
	if text, ok := s.Default.(string); ok {
		return text
	}
	data, err := json.Marshal(s.Default)
	if err != nil {
		return ""
	}
	return string(data)
}

// Media lists the kinds of file the parameter takes directly, as opposed to
// somewhere inside a JSON object.
func (p Param) Media() []string {
	for _, b := range p.Schema.branches() {
		if len(b.MediaKinds) > 0 {
			return b.MediaKinds
		}
		for _, item := range b.Items.branches() {
			if len(item.MediaKinds) > 0 {
				return item.MediaKinds
			}
		}
	}
	return nil
}

// Usage is the parameter's line of help.
func (p Param) Usage() string {
	var parts []string
	if p.Required {
		parts = append(parts, "(required)")
	}
	s := p.Schema.resolve()
	switch {
	case s != nil && s.Description != "":
		parts = append(parts, strings.Join(strings.Fields(s.Description), " "))
	case s != nil && s.Title != "":
		parts = append(parts, s.Title)
	}
	if kinds := p.Media(); len(kinds) > 0 {
		parts = append(parts, "["+strings.Join(kinds, "/")+" URL or local file]")
	}
	if p.TypeName() == "strings" {
		parts = append(parts, "[repeat for several]")
	}
	if p.TypeName() == "json" {
		parts = append(parts, "[JSON, or @file.json]")
	}
	if values := p.Values(); len(values) > 0 {
		parts = append(parts, "[one of: "+strings.Join(values, ", ")+"]")
	}
	if def := p.Default(); def != "" && !p.Required {
		parts = append(parts, "[API default: "+def+"]")
	}
	// pflag reads backticks in a usage string as the flag's value name, and this
	// text is the API's.
	return strings.ReplaceAll(strings.Join(parts, " "), "`", "'")
}

// Coerce turns what was typed for a parameter into the value the API expects.
// A flag given several times is an array. `@path` reads text or JSON from a
// file, which is how a long prompt or a nested object is usually passed.
func Coerce(s *Schema, raws []string) (any, error) {
	if len(raws) == 0 {
		return nil, errors.New("no value")
	}
	branches := s.branches()
	var array *Schema
	var scalars []*Schema
	for _, b := range branches {
		if b.Type == "array" && array == nil {
			array = b
		} else if b.Type != "array" {
			scalars = append(scalars, b)
		}
	}
	if array == nil {
		if len(raws) > 1 {
			return nil, errors.New("given more than once, but it takes one value")
		}
		return coerceOne(branches, raws[0])
	}
	if len(raws) == 1 {
		raw := strings.TrimSpace(raws[0])
		if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "@") {
			v, err := loadJSON(raw)
			if err != nil {
				return nil, err
			}
			if _, ok := v.([]any); !ok {
				return nil, errors.New("expected a JSON array")
			}
			return v, nil
		}
		if len(scalars) > 0 {
			if v, err := coerceOne(scalars, raws[0]); err == nil {
				return v, nil
			}
		}
	}
	items := array.Items.branches()
	out := make([]any, 0, len(raws))
	for _, raw := range raws {
		v, err := coerceOne(items, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// coerceOne tries typed alternatives before plain text, so that "5" is the
// number when a schema allows both a number and a string.
func coerceOne(branches []*Schema, raw string) (any, error) {
	free := len(branches) == 0
	var fixed []string
	for _, b := range branches {
		switch {
		case len(b.Enum) > 0:
			for _, v := range b.Enum {
				if fmt.Sprint(v) == raw {
					return v, nil
				}
				fixed = append(fixed, fmt.Sprint(v))
			}
		case b.Const != nil:
			if fmt.Sprint(b.Const) == raw {
				return b.Const, nil
			}
			fixed = append(fixed, fmt.Sprint(b.Const))
		case b.Type == "integer":
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return n, nil
			}
		case b.Type == "number":
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				return f, nil
			}
		case b.Type == "boolean":
			if raw == "true" || raw == "false" {
				return raw == "true", nil
			}
		case b.Type == "object" || b.Type == "array":
			if t := strings.TrimSpace(raw); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "@") {
				return loadJSON(t)
			}
		case b.Type == "string":
			free = true
		default:
			free = true
		}
	}
	if free {
		// With no type to go by, valid JSON is taken as JSON: 0.2 is a number.
		if len(branches) == 0 || branches[0].Type == "" {
			if v, err := DecodeJSON([]byte(raw)); err == nil {
				return v, nil
			}
		}
		return readText(raw)
	}
	if len(fixed) > 0 {
		return nil, fmt.Errorf("%q is not one of: %s", raw, strings.Join(fixed, ", "))
	}
	kinds := make([]string, 0, len(branches))
	for _, b := range branches {
		kinds = append(kinds, b.Type)
	}
	return nil, fmt.Errorf("%q is not a valid %s", raw, strings.Join(kinds, " or "))
}

// readText expands @path, for text too long to type. A value that merely
// starts with @ and names no file is left as it is.
func readText(raw string) (string, error) {
	if !strings.HasPrefix(raw, "@") || len(raw) == 1 {
		return raw, nil
	}
	data, err := os.ReadFile(raw[1:])
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func loadJSON(raw string) (any, error) {
	data := []byte(raw)
	if strings.HasPrefix(raw, "@") {
		var err error
		if data, err = os.ReadFile(raw[1:]); err != nil {
			return nil, err
		}
	}
	return DecodeJSON(data)
}

// DecodeJSON parses JSON keeping numbers exact, so a large seed survives.
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	return v, nil
}

// Apply sets one named input on a request body. A name the schema does not
// list is refused when the schema lists any, since the API would refuse it too.
func (s *Schema) Apply(body map[string]any, name string, raws []string) error {
	top := s.resolve()
	var property *Schema
	if top != nil {
		property = top.Properties[name]
	}
	if property == nil {
		if top != nil && len(top.Properties) > 0 {
			return fmt.Errorf("%q is not an input of this operation; it takes: %s", name, strings.Join(top.Order, ", "))
		}
		property = &Schema{}
	}
	value, err := Coerce(property, raws)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	body[name] = value
	return nil
}

// Missing lists the required inputs a body does not have.
func (s *Schema) Missing(body map[string]any) []string {
	top := s.resolve()
	if top == nil {
		return nil
	}
	var missing []string
	for _, name := range top.Required {
		if _, ok := body[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// MapMedia calls fn on every string the schema marks as a media input, at any
// depth, and stores what it returns. It walks every branch of a union, so fn
// must leave its own results alone, which a URL check does naturally.
func (s *Schema) MapMedia(v any, fn func(string) (string, error)) (any, error) {
	return mapMedia(s, v, fn, 0)
}

func mapMedia(s *Schema, v any, fn func(string) (string, error), depth int) (any, error) {
	if depth > 32 {
		return v, nil
	}
	for _, b := range s.branches() {
		switch node := v.(type) {
		case string:
			if len(b.MediaKinds) > 0 {
				mapped, err := fn(node)
				if err != nil {
					return nil, err
				}
				return mapped, nil
			}
		case []any:
			if b.Items == nil {
				continue
			}
			for i, item := range node {
				mapped, err := mapMedia(b.Items, item, fn, depth+1)
				if err != nil {
					return nil, err
				}
				node[i] = mapped
			}
		case map[string]any:
			for name, value := range node {
				property := b.Properties[name]
				if property == nil {
					continue
				}
				mapped, err := mapMedia(property, value, fn, depth+1)
				if err != nil {
					return nil, err
				}
				node[name] = mapped
			}
		}
	}
	return v, nil
}
