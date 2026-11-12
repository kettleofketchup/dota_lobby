package api

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/kettleofketchup/dota_lobby/pkg/gc/sim"
)

var updateSpec = flag.Bool("update", false, "rewrite api/openapi.yaml")

// specPath is the committed spec, from pkg/api.
var specPath = filepath.Join("..", "..", "api", "openapi.yaml")

func TestOpenAPISpecUpToDate(t *testing.T) {
	got, err := New(Options{}).OpenAPI().YAML()
	if err != nil {
		t.Fatal(err)
	}
	if *updateSpec {
		if err := os.WriteFile(specPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("%v: run just go::openapi", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("api/openapi.yaml is stale: run just go::openapi and commit it")
	}
}

// The spec does not depend on runtime options, so the committed file
// matches every deployment.
func TestOpenAPISpecIgnoresOptions(t *testing.T) {
	a, err := New(Options{}).OpenAPI().YAML()
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{APIKey: testKey, Version: "v9.9.9", MaxBodyBytes: 1 << 20, Simulator: sim.New(sim.Options{})}).OpenAPI().YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("the spec changes with Options")
	}
}
