// Package ports holds the hexagonal ports for chora-creation: interfaces the
// domain owns and adapters (pg / grpc / http clients) implement. Per the
// hexagonal architecture rule the domain MUST NOT import this package; this
// package may import the domain freely.
//
// Naming convention: one file per port, file name = `{noun}_{role}.go`.
// Each port declares its request/response value types alongside it so the
// adapter has a single import target.
package ports
