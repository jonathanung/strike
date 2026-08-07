package actionfacts

// Hard bounds so adversarial input cannot blow memory or CPU. Exceeding a
// limit yields StatusLimitExceeded (fail closed to non-authoritative).
const (
	maxCommandBytes   = 32 * 1024
	maxArgvItems      = 256
	maxArgvBytes      = 32 * 1024
	maxScalarBytes    = 8 * 1024
	maxCommands       = 64
	maxPaths          = 64
	maxNetwork        = 32
	maxPipelineDepth  = 32
	maxWrapperDepth   = 8
	maxTokens         = 1024
	maxMatchKeyBytes  = 512
	maxMatchKeysTotal = 128
)
