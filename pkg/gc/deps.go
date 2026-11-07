// Package gc is the only package allowed to import go-steam and go-dota2
// (plan §3). Until the adapter lands it just pins the D1 dependency line.
package gc

import (
	// Blank imports keep the upstream masters in go.mod through `go mod tidy`.
	_ "github.com/aperturerobotics/protobuf-go-lite"
	_ "github.com/paralin/go-dota2"
	_ "github.com/paralin/go-steam"
)
