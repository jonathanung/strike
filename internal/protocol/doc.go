// Package protocol is a compatibility re-export of
// github.com/jonathanung/strike-cli/pkg/protocol.
//
// Prefer importing pkg/protocol from new code (SDKs, external frontends, and
// future in-tree call sites). This package keeps existing
// internal/protocol imports compiling without a mass rewrite; types and
// functions are identical to the public package (type aliases / thin
// forwards).
//
// Stability and wire-schema versioning are documented on pkg/protocol
// ([pkg/protocol.Version]).
package protocol
