module github.com/jonathanung/strike-cli/providers

go 1.26.2

require github.com/jonathanung/strike-cli/harness v0.0.0

replace github.com/jonathanung/strike-cli/harness => ../harness

replace github.com/jonathanung/strike-cli/pkg/protocol => ../pkg/protocol

replace github.com/jonathanung/strike-cli/pkg/redact => ../pkg/redact
