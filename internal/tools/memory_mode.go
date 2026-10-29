package tools

import (
	"errors"
	"strings"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/persist/memory"
)

func sessionMemoryMode(tc *tool.Context) string {
	if tc == nil || strings.TrimSpace(tc.MemoryMode) == "" {
		return memory.ModeNormal
	}
	return tc.MemoryMode
}

func rejectMemoryWrite(tc *tool.Context) error {
	mode := sessionMemoryMode(tc)
	if memory.AllowsWrite(mode) {
		return nil
	}
	if _, err := memory.NormalizeMode(mode); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(mode), memory.ModeIncognito) {
		return errors.New("memory mode incognito: this session reads memory but does not write it")
	}
	return errors.New("memory mode temporary: this session has no memory")
}

func rejectMemoryRead(tc *tool.Context) error {
	mode := sessionMemoryMode(tc)
	if memory.AllowsRead(mode) {
		return nil
	}
	if _, err := memory.NormalizeMode(mode); err != nil {
		return err
	}
	return errors.New("memory mode temporary: this session has no memory")
}
