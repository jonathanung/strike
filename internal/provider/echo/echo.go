// Package echo re-exports the offline development provider.
package echo

import pub "github.com/jonathanung/strike-cli/provider/echo"

type Provider = pub.Provider

func New() Provider { return pub.New() }
