package host

// Skill is a slash-command prompt template. render is injected so the
// contract stays free of template syntax.
type Skill struct {
	Name        string
	Description string
	HasArgs     bool // template contains $ARGUMENTS (frontends show an args hint)
	render      func(args string) string
}

// NewSkill builds a Skill from its display fields and a render function. The
// render function may be nil, in which case Render returns "".
func NewSkill(name, description string, hasArgs bool, render func(string) string) Skill {
	return Skill{Name: name, Description: description, HasArgs: hasArgs, render: render}
}

// Render returns the prompt to submit; returns "" when no renderer was set.
func (s Skill) Render(args string) string {
	if s.render == nil {
		return ""
	}
	return s.render(args)
}
