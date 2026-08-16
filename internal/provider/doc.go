// Package provider is a compatibility re-export of
// github.com/jonathanung/strike-cli/provider.
//
// Prefer the public provider module from new code (and from the providers
// module, which cannot import internal/). This package keeps existing
// internal/provider imports compiling without a mass rewrite.
package provider
