package main

import (
	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/internal/frontend/server"
	"github.com/jonathanung/strike/internal/persist/memory"
	"github.com/jonathanung/strike/internal/persist/session"
)

// attachMemoryMode binds the web session flag to the engine and the session
// sidecar so a restart restores incognito or temporary.
func (a *assembled) attachMemoryMode(live *server.Live, id string, eng *engine.Engine) {
	if a == nil || live == nil || eng == nil || id == "" {
		return
	}
	live.BindMemoryMode(
		func() string { return eng.MemoryMode() },
		func(mode string) error {
			norm, err := memory.NormalizeMode(mode)
			if err != nil {
				return err
			}
			if err := eng.SetMemoryMode(norm); err != nil {
				return err
			}
			if a.sessions == nil {
				return nil
			}
			meta, err := session.ReadMeta(a.sessions.Dir(), id)
			if err != nil {
				return err
			}
			meta.MemoryMode = norm
			return session.WriteMeta(a.sessions.Dir(), id, meta)
		},
	)
}
