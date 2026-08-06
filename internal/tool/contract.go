package tool

import (
	"fmt"
)

// ContractVersion is the current built-in tool contract schema version.
// Bump when Contract fields change meaning.
const ContractVersion = 1

// SideEffect classifies what a tool may do outside pure computation.
// Values are stable wire/API tokens (kebab-case).
type SideEffect string

const (
	SideEffectNone              SideEffect = "none"
	SideEffectRead              SideEffect = "read"
	SideEffectWorkspaceMutative SideEffect = "workspace-mutative"
	SideEffectProcess           SideEffect = "process"
	SideEffectNetwork           SideEffect = "network"
	SideEffectExternal          SideEffect = "external"
)

// Idempotency describes whether a failed or interrupted call is safe to retry.
type Idempotency string

const (
	// IdempotencySafeRetry: repeating the call with the same args is safe
	// (reads, pure queries, no durable side effects).
	IdempotencySafeRetry Idempotency = "safe-retry"
	// IdempotencyConditional: retry may be safe after checking state
	// (e.g. edit when oldString still matches).
	IdempotencyConditional Idempotency = "conditional"
	// IdempotencyUnsafe: retry can double-apply side effects (bash, network POSTs).
	IdempotencyUnsafe Idempotency = "unsafe"
)

// ValidSideEffect reports whether s is a known side-effect class.
func ValidSideEffect(s SideEffect) bool {
	switch s {
	case SideEffectNone, SideEffectRead, SideEffectWorkspaceMutative,
		SideEffectProcess, SideEffectNetwork, SideEffectExternal:
		return true
	}
	return false
}

// ValidIdempotency reports whether i is a known idempotency class.
func ValidIdempotency(i Idempotency) bool {
	switch i {
	case IdempotencySafeRetry, IdempotencyConditional, IdempotencyUnsafe:
		return true
	}
	return false
}

// Contract is the static declaration for one tool: versioned side-effect and
// idempotency metadata. Input JSON Schema remains Tool.Schema(); optional
// output schema may be added in a later contract version.
type Contract struct {
	Version     int         `json:"version"`
	SideEffect  SideEffect  `json:"sideEffect"`
	Idempotency Idempotency `json:"idempotency"`
}

// Validate checks contract field vocabulary and version.
func (c Contract) Validate() error {
	if c.Version < 1 {
		return fmt.Errorf("contract version must be >= 1")
	}
	if !ValidSideEffect(c.SideEffect) {
		return fmt.Errorf("unknown sideEffect %q", c.SideEffect)
	}
	if !ValidIdempotency(c.Idempotency) {
		return fmt.Errorf("unknown idempotency %q", c.Idempotency)
	}
	return nil
}

// Contractor is optionally implemented by tools that declare a static Contract.
// Tools without Contractor receive DefaultContract via LookupContract.
type Contractor interface {
	Contract() Contract
}

// DefaultContract is used when a tool does not implement Contractor
// (unknown/plugin tools default to external + conditional).
func DefaultContract() Contract {
	return Contract{
		Version:     ContractVersion,
		SideEffect:  SideEffectExternal,
		Idempotency: IdempotencyConditional,
	}
}

// LookupContract returns t's declared contract, or DefaultContract.
func LookupContract(t Tool) Contract {
	if t == nil {
		return DefaultContract()
	}
	if c, ok := t.(Contractor); ok {
		return c.Contract()
	}
	return DefaultContract()
}

// staticContract builds a v1 contract (shared by built-in Contract methods).
func staticContract(se SideEffect, id Idempotency) Contract {
	return Contract{Version: ContractVersion, SideEffect: se, Idempotency: id}
}
