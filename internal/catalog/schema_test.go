package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fixtures are real catalog answers with their prose removed, so these tests
// fail if the code stops matching the shapes Pika actually sends.
func loadOp(t *testing.T, name string) (*Op, *Schema) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var op Op
	if err := json.Unmarshal(data, &op); err != nil {
		t.Fatal(err)
	}
	schema, err := ParseSchema(op.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	return &op, schema
}

func TestParamsKeepTheAPIsOrderWithRequiredFirst(t *testing.T) {
	_, schema := loadOp(t, "pika_pika-h3-flex_text-to-video")
	var names []string
	for _, p := range schema.Params() {
		names = append(names, p.Name)
	}
	want := []string{"prompt", "duration", "aspect_ratio", "resolution", "seed", "rewrite", "output_mode", "channel_mode"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("params = %v, want %v", names, want)
	}
	if !schema.Params()[0].Required {
		t.Error("prompt should be required")
	}
}

func TestCoerceUsesTheSchemasTypes(t *testing.T) {
	_, schema := loadOp(t, "pika_pika-h3-flex_text-to-video")
	tests := []struct {
		name string
		raws []string
		want any
	}{
		{"duration", []string{"10"}, int64(10)},
		{"rewrite", []string{"false"}, false},
		{"resolution", []string{"768p"}, "768p"},
		{"aspect_ratio", []string{"9:16"}, "9:16"}, // an enum inside anyOf with null
		{"prompt", []string{"5"}, "5"},             // a string stays a string
	}
	for _, tt := range tests {
		body := map[string]any{}
		if err := schema.Apply(body, tt.name, tt.raws); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if !reflect.DeepEqual(body[tt.name], tt.want) {
			t.Errorf("%s = %#v, want %#v", tt.name, body[tt.name], tt.want)
		}
	}
}

func TestCoerceRejectsWhatTheSchemaRules(t *testing.T) {
	_, schema := loadOp(t, "pika_pika-h3-flex_text-to-video")
	tests := []struct {
		name string
		raws []string
		want string
	}{
		{"resolution", []string{"9000p"}, "not one of"},
		{"duration", []string{"soon"}, "not a valid integer"},
		{"duration", []string{"5", "6"}, "more than once"},
		{"nope", []string{"1"}, "not an input"},
	}
	for _, tt := range tests {
		err := schema.Apply(map[string]any{}, tt.name, tt.raws)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %v: error = %v, want it to mention %q", tt.name, tt.raws, err, tt.want)
		}
	}
}

func TestCoerceUnionsPreferTheTypedAlternative(t *testing.T) {
	schema, err := ParseSchema(json.RawMessage(`{"properties":{
		"either":{"anyOf":[{"type":"string"},{"type":"integer"}]},
		"count":{"enum":[5,10]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	if err := schema.Apply(body, "either", []string{"7"}); err != nil {
		t.Fatal(err)
	}
	if body["either"] != int64(7) {
		t.Errorf("either = %#v, want the integer", body["either"])
	}
	if err := schema.Apply(body, "either", []string{"auto"}); err != nil {
		t.Fatal(err)
	}
	if body["either"] != "auto" {
		t.Errorf("either = %#v, want the string", body["either"])
	}
	if err := schema.Apply(body, "count", []string{"10"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(body["count"]); string(data) != "10" {
		t.Errorf("count = %s, want a JSON number", data)
	}
}

func TestArraysFromRepeatsOrJSONOrAFile(t *testing.T) {
	_, schema := loadOp(t, "pika_video-merge_merge-videos")
	body := map[string]any{}
	if err := schema.Apply(body, "videos", []string{"https://a/1.mp4", "https://a/2.mp4"}); err != nil {
		t.Fatal(err)
	}
	if got := body["videos"].([]any); len(got) != 2 || got[1] != "https://a/2.mp4" {
		t.Errorf("repeated flag = %v", got)
	}
	if err := schema.Apply(body, "videos", []string{`["https://a/3.mp4"]`}); err != nil {
		t.Fatal(err)
	}
	if got := body["videos"].([]any); len(got) != 1 || got[0] != "https://a/3.mp4" {
		t.Errorf("JSON literal = %v", got)
	}
	path := filepath.Join(t.TempDir(), "videos.json")
	if err := os.WriteFile(path, []byte(`["https://a/4.mp4"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := schema.Apply(body, "videos", []string{"@" + path}); err != nil {
		t.Fatal(err)
	}
	if got := body["videos"].([]any); got[0] != "https://a/4.mp4" {
		t.Errorf("@file = %v", got)
	}
}

func TestLongTextFromAFile(t *testing.T) {
	_, schema := loadOp(t, "pika_pika-h3-flex_text-to-video")
	path := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(path, []byte("a long prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	if err := schema.Apply(body, "prompt", []string{"@" + path}); err != nil {
		t.Fatal(err)
	}
	if body["prompt"] != "a long prompt" {
		t.Errorf("prompt = %#v", body["prompt"])
	}
	// A prompt that merely starts with @ is still a prompt.
	if err := schema.Apply(body, "prompt", []string{"@nobody said this"}); err != nil {
		t.Fatal(err)
	}
	if body["prompt"] != "@nobody said this" {
		t.Errorf("prompt = %#v", body["prompt"])
	}
}

func TestMapMediaReachesEveryDepth(t *testing.T) {
	upper := func(s string) (string, error) {
		if strings.HasPrefix(s, "https://") {
			return s, nil
		}
		return "https://hosted/" + s, nil
	}

	// Plain, in anyOf with null, and in an array in anyOf.
	_, h3 := loadOp(t, "pika_pika-h3-flex_image-to-video")
	body := map[string]any{"prompt": "a.png", "image": "a.png", "last_frame_image": "b.png"}
	if _, err := h3.MapMedia(body, upper); err != nil {
		t.Fatal(err)
	}
	if body["image"] != "https://hosted/a.png" || body["last_frame_image"] != "https://hosted/b.png" {
		t.Errorf("anyOf media not mapped: %v", body)
	}
	if body["prompt"] != "a.png" {
		t.Errorf("a non-media input was changed: %v", body["prompt"])
	}

	// In a oneOf of $refs, in an array, in an array: the compose timeline.
	_, compose := loadOp(t, "pika_video-compose_compose-video")
	timeline, err := DecodeJSON([]byte(`{"tracks":[
		{"id":"v","type":"video","keyframes":[{"timestamp":0,"duration":1000,"url":"clip.mp4"}]},
		{"id":"a","type":"audio","keyframes":[{"timestamp":0,"duration":1000,"url":"https://a/song.mp3"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compose.MapMedia(timeline, upper); err != nil {
		t.Fatal(err)
	}
	tracks := timeline.(map[string]any)["tracks"].([]any)
	first := tracks[0].(map[string]any)["keyframes"].([]any)[0].(map[string]any)
	if first["url"] != "https://hosted/clip.mp4" {
		t.Errorf("nested media not mapped: %v", first)
	}
	if id := tracks[0].(map[string]any)["id"]; id != "v" {
		t.Errorf("a track id was changed: %v", id)
	}
	second := tracks[1].(map[string]any)["keyframes"].([]any)[0].(map[string]any)
	if second["url"] != "https://a/song.mp3" {
		t.Errorf("a URL was changed: %v", second)
	}
}

func TestAPropertiesObjectBesideAnAnyOfKeepsItsProperties(t *testing.T) {
	schema, err := ParseSchema(json.RawMessage(`{"type":"object",
		"properties":{"image":{"type":"string","media_kinds":["image"]}},
		"anyOf":[{"required":["image"]},{"required":["prompt"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"image": "a.png"}
	if _, err := schema.MapMedia(body, func(string) (string, error) { return "https://hosted/a.png", nil }); err != nil {
		t.Fatal(err)
	}
	if body["image"] != "https://hosted/a.png" {
		t.Errorf("image = %v", body["image"])
	}
	if len(schema.branches()) != 1 {
		t.Errorf("a branch that only lists required names is not a shape: %d branches", len(schema.branches()))
	}
}

func TestEmptySchemaAcceptsAnything(t *testing.T) {
	schema, err := ParseSchema(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	for _, set := range [][2]string{{"temperature", "0.2"}, {"stream", "true"}, {"stop", `["a"]`}, {"user", "someone"}} {
		if err := schema.Apply(body, set[0], []string{set[1]}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"stop":["a"],"stream":true,"temperature":0.2,"user":"someone"}`
	if string(data) != want {
		t.Errorf("body = %s, want %s", data, want)
	}
}

func TestLargeSeedStaysExact(t *testing.T) {
	v, err := DecodeJSON([]byte(`{"seed":9223372036854775807}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"seed":9223372036854775807}` {
		t.Errorf("seed came back as %s", data)
	}
}

// TestEverySchemaBuildsFlags runs over the fixtures, and over a complete dump
// of the catalog when PIKACTL_SCHEMA_DIR points at one.
func TestEverySchemaBuildsFlags(t *testing.T) {
	dirs := []string{"testdata"}
	if dir := os.Getenv("PIKACTL_SCHEMA_DIR"); dir != "" {
		dirs = append(dirs, dir)
	}
	checked := 0
	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var op Op
			if err := json.Unmarshal(data, &op); err != nil {
				t.Errorf("%s: %v", path, err)
				continue
			}
			schema, err := ParseSchema(op.InputSchema)
			if err != nil {
				t.Errorf("%s: %v", op.ID, err)
				continue
			}
			seen := map[string]bool{}
			for _, p := range schema.Params() {
				if seen[p.Name] {
					t.Errorf("%s: %s listed twice", op.ID, p.Name)
				}
				seen[p.Name] = true
				if strings.Contains(p.Usage(), "`") {
					t.Errorf("%s --%s: a backtick in the usage would break the help", op.ID, p.Name)
				}
				if p.TypeName() == "" {
					t.Errorf("%s --%s: no type", op.ID, p.Name)
				}
			}
			_ = op.PriceLines()
			_ = op.PriceFrom()
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no schemas were checked")
	}
	t.Logf("checked %d operations", checked)
}

func TestPrices(t *testing.T) {
	op, _ := loadOp(t, "pika_pika-h3-flex_text-to-video")
	if got := op.PriceFrom(); got != "$0.01/second" {
		t.Errorf("PriceFrom = %q", got)
	}
	lines := op.PriceLines()
	if len(lines) != 5 || !strings.HasPrefix(lines[1], "$0.02/second") || !strings.Contains(lines[1], "resolution=768p") {
		t.Errorf("PriceLines = %v", lines)
	}
	if got := (&Op{}).PriceFrom(); got != "" {
		t.Errorf("no pricing should render as nothing, got %q", got)
	}
}

func TestPriceAt(t *testing.T) {
	op, _ := loadOp(t, "pika_pika-h3-flex_text-to-video")
	for _, c := range []struct {
		spec map[string]any
		want string
	}{
		{map[string]any{"resolution": "768p"}, "$0.02/second"},
		{map[string]any{"resolution": "8k"}, ""},
		{map[string]any{}, "from $0.01/second"},
	} {
		if got := op.PriceAt(c.spec); got != c.want {
			t.Errorf("PriceAt(%v) = %q, want %q", c.spec, got, c.want)
		}
	}
	// A tier that names more settings than the row still fits it.
	extra := &Op{Pricing: json.RawMessage(`{"components":[{"unit":{"type":"second","quantity":1},"price_tiers":[
		{"spec":{"resolution":"480p","mode":"fast"},"micro_usd":30000},
		{"spec":{"resolution":"480p","mode":"standard"},"micro_usd":50000},
		{"spec":{"resolution":"720p","mode":"standard"},"micro_usd":70000}]}]}`)}
	if got := extra.PriceAt(map[string]any{"resolution": "720p"}); got != "$0.07/second" {
		t.Errorf("one fitting tier = %q", got)
	}
	if got := extra.PriceAt(map[string]any{"resolution": "480p"}); got != "from $0.03/second" {
		t.Errorf("two fitting tiers = %q", got)
	}
}

// A price with no fixed rate is described in words; show those rather than nothing.
func TestPriceWithoutRate(t *testing.T) {
	op, _ := loadOp(t, "pika_pika-h3-flex_draft-to-video")
	if got := op.PriceFrom(); got != "quote" {
		t.Errorf("PriceFrom = %q", got)
	}
	lines := op.PriceLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "minus the amount paid") {
		t.Errorf("PriceLines = %v", lines)
	}
}

func TestParseEndpoint(t *testing.T) {
	spec := "# Model\n\n## Endpoint\n\n```\nPOST https://api.dev.pika.art/anthropic/v1/messages\n```\n\n" +
		"Required headers:\n- `anthropic-version: 2023-06-01`\n\n## Request\n- `not-a-header: nope`\n"
	ep, err := ParseEndpoint("x", spec)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Path != "/anthropic/v1/messages" {
		t.Errorf("path = %q", ep.Path)
	}
	want := map[string]string{"anthropic-version": "2023-06-01"}
	if !reflect.DeepEqual(ep.Headers, want) {
		t.Errorf("headers = %v, want %v", ep.Headers, want)
	}
	if _, err := ParseEndpoint("x", "no endpoint here"); err == nil {
		t.Error("expected an error for a spec with no endpoint")
	}
}

func TestCheckID(t *testing.T) {
	for _, id := range []string{"../../etc/passwd", "a/../b", "a?b=c", "a b", "/abs", ""} {
		if checkID(id) == nil {
			t.Errorf("%q should be refused", id)
		}
	}
	for _, id := range []string{"pika/pika-h3-flex/text-to-video", "anthropic/claude-opus-5.5", "typesafe/jev-1.13"} {
		if err := checkID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
}
