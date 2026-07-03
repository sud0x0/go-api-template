// Package contract holds tests that assert the OpenAPI spec
// (api/v1/openapi.yaml) and the Go code agree on shared, documented invariants.
//
// These are the tripwires that keep "the spec is the contract" honest: if a
// future change makes the code and the spec disagree about the pagination
// default, the maximum page size, or the documented log length limit, one of
// these tests fails. They are pure-Go (no network, no DB) so they run in the
// normal `go test ./...` sweep and in `make ci`.
package contract

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sud0x0/go-api-template/internal/shared"
)

// openAPIPath is relative to this test file's directory (internal/contract).
const openAPIPath = "../../api/v1/openapi.yaml"

// openAPISpec models only the slices of the OpenAPI document these tests read.
// yaml.Unmarshal ignores every field not declared here.
type openAPISpec struct {
	// Paths maps a URL → (HTTP method → operation).
	Paths      map[string]map[string]operation `yaml:"paths"`
	Components struct {
		Schemas map[string]schema `yaml:"schemas"`
	} `yaml:"components"`
}

type operation struct {
	Parameters []parameter `yaml:"parameters"`
}

type parameter struct {
	Name   string `yaml:"name"`
	In     string `yaml:"in"`
	Schema schema `yaml:"schema"`
}

type schema struct {
	Default    *int              `yaml:"default"`
	Maximum    *int              `yaml:"maximum"`
	Minimum    *int              `yaml:"minimum"`
	MaxLength  *int              `yaml:"maxLength"`
	MaxItems   *int              `yaml:"maxItems"`
	Properties map[string]schema `yaml:"properties"`
}

func loadSpec(t *testing.T) openAPISpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(openAPIPath))
	if err != nil {
		t.Fatalf("read OpenAPI spec %s: %v", openAPIPath, err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse OpenAPI spec: %v", err)
	}
	return spec
}

// TestOpenAPIPaginationMatchesSharedConstants asserts the documented `limit`
// query parameter's default and maximum equal shared.DefaultPageSize and
// shared.MaxPageSize. (Task 1's contract tripwire.)
func TestOpenAPIPaginationMatchesSharedConstants(t *testing.T) {
	spec := loadSpec(t)

	op, ok := spec.Paths["/api/v1/logs"]["get"]
	if !ok {
		t.Fatal("OpenAPI spec is missing GET /api/v1/logs")
	}

	var limitParam *parameter
	for i := range op.Parameters {
		if op.Parameters[i].Name == "limit" && op.Parameters[i].In == "query" {
			limitParam = &op.Parameters[i]
			break
		}
	}
	if limitParam == nil {
		t.Fatal("GET /api/v1/logs is missing the `limit` query parameter in the spec")
	}

	if limitParam.Schema.Default == nil {
		t.Fatal("spec `limit` parameter has no documented default")
	}
	if *limitParam.Schema.Default != shared.DefaultPageSize {
		t.Errorf("spec limit default = %d, want shared.DefaultPageSize (%d)",
			*limitParam.Schema.Default, shared.DefaultPageSize)
	}

	if limitParam.Schema.Maximum == nil {
		t.Fatal("spec `limit` parameter has no documented maximum")
	}
	if *limitParam.Schema.Maximum != shared.MaxPageSize {
		t.Errorf("spec limit maximum = %d, want shared.MaxPageSize (%d)",
			*limitParam.Schema.Maximum, shared.MaxPageSize)
	}
}

// queryParam returns the named query parameter of an operation, or nil.
func queryParam(op operation, name string) *parameter {
	for i := range op.Parameters {
		if op.Parameters[i].Name == name && op.Parameters[i].In == "query" {
			return &op.Parameters[i]
		}
	}
	return nil
}

// TestOpenAPIPaginationModesDocumented asserts the GET /api/v1/logs spec
// documents BOTH pagination modes the code implements: the keyset `cursor`
// query parameter exists, and the `offset` parameter's documented maximum
// equals shared.MaxPageOffset (the hard reachability cap). Tripwires for the
// cursor/offset surface so the spec can't silently drift from the handler.
func TestOpenAPIPaginationModesDocumented(t *testing.T) {
	spec := loadSpec(t)

	op, ok := spec.Paths["/api/v1/logs"]["get"]
	if !ok {
		t.Fatal("OpenAPI spec is missing GET /api/v1/logs")
	}

	if queryParam(op, "cursor") == nil {
		t.Error("GET /api/v1/logs is missing the `cursor` query parameter — cursor pagination is undocumented")
	}

	offsetParam := queryParam(op, "offset")
	if offsetParam == nil {
		t.Fatal("GET /api/v1/logs is missing the `offset` query parameter")
	}
	if offsetParam.Schema.Maximum == nil {
		t.Fatal("spec `offset` parameter has no documented maximum (the hard depth cap)")
	}
	if *offsetParam.Schema.Maximum != shared.MaxPageOffset {
		t.Errorf("spec offset maximum = %d, want shared.MaxPageOffset (%d)",
			*offsetParam.Schema.Maximum, shared.MaxPageOffset)
	}

	// The cursor-mode response wrapper must be documented as a schema.
	if _, ok := spec.Components.Schemas["LogCursorPage"]; !ok {
		t.Error("OpenAPI spec is missing the LogCursorPage schema (cursor-mode response shape)")
	}
}

// TestOpenAPILogMaxLengthMatchesSharedConstant asserts the documented
// `maxLength` on the `log` property of both request schemas equals
// shared.LogMaxChars — so the spec's advertised length limit and the server's
// enforced one (the service checks shared.LogMaxChars) cannot silently diverge.
func TestOpenAPILogMaxLengthMatchesSharedConstant(t *testing.T) {
	spec := loadSpec(t)

	for _, name := range []string{"LogCreateRequest", "LogUpdateRequest"} {
		t.Run(name, func(t *testing.T) {
			sch, ok := spec.Components.Schemas[name]
			if !ok {
				t.Fatalf("OpenAPI spec is missing schema %q", name)
			}
			logProp, ok := sch.Properties["log"]
			if !ok {
				t.Fatalf("%s has no `log` property in the spec", name)
			}
			if logProp.MaxLength == nil {
				t.Fatalf("%s.log has no documented maxLength", name)
			}
			if *logProp.MaxLength != shared.LogMaxChars {
				t.Errorf("spec %s.log maxLength = %d, want shared.LogMaxChars (%d)",
					name, *logProp.MaxLength, shared.LogMaxChars)
			}
		})
	}

	// The LimitExceededError schema's `limit` example is intentionally NOT
	// asserted: that schema is defined with `allOf` (composition), which the
	// minimal structs in this test do not model, so the example is not trivially
	// reachable through them. Examples are illustrative; the schema (the
	// maxLength asserted above) is the contract.
}

// TestOpenAPIBatchMaxMatchesSharedConstant asserts the documented `maxItems` on
// the batch-create request's `logs` array equals shared.MaxBatchSize, so the
// spec's advertised batch limit and the server's enforced one cannot diverge.
func TestOpenAPIBatchMaxMatchesSharedConstant(t *testing.T) {
	spec := loadSpec(t)

	sch, ok := spec.Components.Schemas["LogBatchCreateRequest"]
	if !ok {
		t.Fatal("OpenAPI spec is missing schema LogBatchCreateRequest")
	}
	logs, ok := sch.Properties["logs"]
	if !ok {
		t.Fatal("LogBatchCreateRequest has no `logs` property in the spec")
	}
	if logs.MaxItems == nil {
		t.Fatal("LogBatchCreateRequest.logs has no documented maxItems")
	}
	if *logs.MaxItems != shared.MaxBatchSize {
		t.Errorf("spec LogBatchCreateRequest.logs maxItems = %d, want shared.MaxBatchSize (%d)",
			*logs.MaxItems, shared.MaxBatchSize)
	}
}
