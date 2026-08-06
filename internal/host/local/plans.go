package local

import (
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/plan"
)

// NewPlans adapts *plan.Store to host.Plans. A nil store yields nil.
func NewPlans(store *plan.Store) host.Plans {
	if store == nil {
		return nil
	}
	return plansAdapter{store: store}
}

type plansAdapter struct {
	store *plan.Store
}

func (a plansAdapter) List() ([]host.PlanMeta, error) {
	list, err := a.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.PlanMeta, len(list))
	for i, m := range list {
		out[i] = toHostMeta(m)
	}
	return out, nil
}

func (a plansAdapter) Get(id string) (host.Plan, bool, error) {
	p, ok, err := a.store.Get(id)
	if err != nil || !ok {
		return host.Plan{}, ok, err
	}
	return toHostPlan(p), true, nil
}

func (a plansAdapter) Create(ownerRoot, title string, sections []host.PlanSection) (host.Plan, error) {
	in := make([]plan.SectionInput, len(sections))
	for i, s := range sections {
		in[i] = plan.SectionInput{Title: s.Title, Body: s.Body}
	}
	p, err := a.store.Create(ownerRoot, title, in)
	if err != nil {
		return host.Plan{}, err
	}
	return toHostPlan(p), nil
}

func (a plansAdapter) UpdateTitle(id, ownerRoot, title string, expectedVersion int) (host.Plan, error) {
	p, err := a.store.UpdateTitle(id, ownerRoot, title, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	return toHostPlan(p), nil
}

func (a plansAdapter) UpdateSection(id, ownerRoot, sectionID string, title, body *string, expectedVersion int) (host.Plan, error) {
	p, err := a.store.UpdateSection(id, ownerRoot, sectionID, title, body, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	return toHostPlan(p), nil
}

func (a plansAdapter) AddSection(id, ownerRoot, title, body string, expectedVersion int) (host.Plan, error) {
	p, err := a.store.AddSection(id, ownerRoot, title, body, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	return toHostPlan(p), nil
}

func (a plansAdapter) SetStatus(id, ownerRoot, status string, expectedVersion int) (host.Plan, error) {
	p, err := a.store.SetStatus(id, ownerRoot, status, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	return toHostPlan(p), nil
}

func (a plansAdapter) Reopen(id, ownerRoot string, expectedVersion int) (host.Plan, error) {
	p, err := a.store.Reopen(id, ownerRoot, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	return toHostPlan(p), nil
}

func toHostPlan(p plan.Plan) host.Plan {
	out := host.Plan{
		ID:        p.ID,
		OwnerRoot: p.OwnerRoot,
		Title:     p.Title,
		Status:    p.Status,
		Version:   p.Version,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	if p.Sections != nil {
		out.Sections = make([]host.PlanSection, len(p.Sections))
		for i, s := range p.Sections {
			out.Sections[i] = host.PlanSection{ID: s.ID, Title: s.Title, Body: s.Body}
		}
	}
	return out
}

func toHostMeta(m plan.Meta) host.PlanMeta {
	return host.PlanMeta{
		ID:           m.ID,
		OwnerRoot:    m.OwnerRoot,
		Title:        m.Title,
		Status:       m.Status,
		Version:      m.Version,
		SectionCount: m.SectionCount,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}
