package main

import (
	"strings"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/internal/composer/triggers"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/product/config"
)

// composerPrepare expands user.input / steer text for every root engine.
// dir is the session working directory. skills are the admitted catalog.
func composerPrepare(text, dir string, skills []config.Skill) engine.InputPrep {
	var files host.Files
	if strings.TrimSpace(dir) != "" {
		files = local.NewFiles(dir)
	}
	views := make([]triggers.Skill, len(skills))
	for i, sk := range skills {
		sk := sk
		views[i] = triggers.Skill{
			Name:        sk.Name,
			Description: sk.Description,
			Render:      sk.Render,
		}
	}
	out := triggers.Prepare(text, files, views)
	return engine.InputPrep{
		Action:    engine.InputAction(out.Action),
		ModelText: out.ModelText,
		Display:   out.Display,
		Shell:     out.Shell,
		Error:     out.Error,
		Op:        out.Op,
		Notices:   out.Notices,
	}
}
