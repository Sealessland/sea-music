// Package appapi adapts HTTP requests to the application's domain services.
//
// Handlers in this package own transport concerns such as authentication,
// request decoding, validation errors, route registration, and response
// mapping. Process lifecycle and cross-domain wiring belong in cmd/api; domain
// rules and persistence behavior belong in their respective domain packages.
package appapi
