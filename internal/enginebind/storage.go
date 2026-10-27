package enginebind

import (
	"strings"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/internal/persist/knowledge"
)

// CombineMemory concatenates auto-load text from each source. Nil parts are
// skipped. A single remaining source is returned as itself.
func CombineMemory(parts ...engine.MemorySource) engine.MemorySource {
	keep := make([]engine.MemorySource, 0, len(parts))
	for _, p := range parts {
		if p != nil {
			keep = append(keep, p)
		}
	}
	switch len(keep) {
	case 0:
		return nil
	case 1:
		return keep[0]
	default:
		return combinedMemory{parts: keep}
	}
}

type combinedMemory struct {
	parts []engine.MemorySource
}

func (c combinedMemory) AutoLoad() (string, int, error) {
	var b strings.Builder
	omitted := 0
	for _, p := range c.parts {
		text, n, err := p.AutoLoad()
		if err != nil {
			return "", omitted, err
		}
		omitted += n
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	return b.String(), omitted, nil
}

// Knowledge adapts a pin store onto the memory auto-load seam. The prompt
// receives paths and notes, not file bodies.
func Knowledge(s *knowledge.Store) engine.MemorySource {
	if s == nil {
		return nil
	}
	return knowledgeBind{s}
}

type knowledgeBind struct{ *knowledge.Store }

func (k knowledgeBind) AutoLoad() (string, int, error) {
	pins, err := k.List()
	if err != nil {
		return "", 0, err
	}
	text, omitted := knowledge.FormatAutoLoad(k.Scope(), pins)
	return text, omitted, nil
}
