package server

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/persist/suggestion"
	"github.com/jonathanung/strike/internal/product/skillpropose"
)

// Skill proposals follow-up (#1913): a slow weekly scan for workspaces that
// opted in, and the Ideas inbox (#1323) as a second home for the cards.
// The cadence checks hourly which opted-in workspaces are a week past
// their last scan; it reads nothing for a workspace whose opt-in is off.

const (
	skillProposalTick = time.Hour
	// proposalIdeaSource is the Ideas inbox source for mirrored drafts.
	proposalIdeaSource = "skill-proposals"
	proposalIdeaKind   = "skill-proposal"
	proposalIdeaMaxEv  = 10
)

// RunSkillProposalCadence scans due workspaces now and then hourly until
// ctx ends. Read-only attach mode never scans.
func (s *Server) RunSkillProposalCadence(ctx context.Context) {
	if s == nil || s.opts.ReadOnly {
		return
	}
	s.scanDueSkillProposals(time.Now())
	t := time.NewTicker(skillProposalTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.scanDueSkillProposals(now)
		}
	}
}

// scanDueSkillProposals scans every workspace DueWeekly at now and mirrors
// the resulting drafts into the Ideas inbox. It returns how many
// workspaces it scanned.
func (s *Server) scanDueSkillProposals(now time.Time) int {
	if s == nil || s.opts.ReadOnly {
		return 0
	}
	store := proposalStore()
	due, err := store.Due(now)
	if err != nil {
		return 0
	}
	scanned := 0
	for _, d := range due {
		wfs, err := skillpropose.ReadWorkflows(s.opts.SessionDir, d.Key, now.Add(-skillpropose.DefaultWindow))
		if err != nil {
			continue
		}
		ws, err := store.Scan(d.Key, wfs, installedSkills(d.Dir), now)
		if err != nil {
			continue
		}
		scanned++
		ws = s.modelDraftProposals(context.Background(), d.Key, ws)
		s.mirrorProposalIdeas(ws.Pending)
	}
	return scanned
}

// proposalIdeaKey is the Ideas evidence key for one draft: one inbox row
// per pattern, however many scans find it.
func proposalIdeaKey(id string) string { return "skill-proposal:" + id }

func clampText(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// proposalIdea is the Ideas inbox draft for one pending skill proposal.
func proposalIdea(p skillpropose.Proposal) suggestion.Draft {
	n := p.Count
	if n == 0 {
		n = len(p.Sessions)
	}
	var ev []suggestion.Evidence
	for _, e := range p.Sessions {
		if len(ev) == proposalIdeaMaxEv {
			break
		}
		sid := strings.TrimSpace(e.SessionID)
		if sid == "" || len(sid) > 128 || strings.ContainsAny(sid, "/\\ \t\r\n") {
			continue
		}
		label := strings.TrimSpace(e.Title)
		if label == "" {
			label = "Session"
		}
		ev = append(ev, suggestion.Evidence{Label: clampText(label, 200), SessionID: sid})
	}
	why := fmt.Sprintf("%d sessions opened with a similar request and ran similar tools.", n)
	if desc := strings.TrimSpace(p.Description); desc != "" {
		why = clampText(desc, 1500) + " " + why
	}
	return suggestion.Draft{
		Title:       clampText("Skill proposal: "+p.Name, 120),
		Why:         why,
		Prompt:      fmt.Sprintf("Review the drafted skill %q in Customize → Skills and accept, edit or dismiss it. It was drafted from %d sessions that repeated the same workflow.", p.Name, n),
		Source:      proposalIdeaSource,
		Kind:        proposalIdeaKind,
		EvidenceKey: proposalIdeaKey(p.ID),
		Evidence:    ev,
	}
}

// mirrorProposalIdeas files each pending draft in the Ideas inbox. The
// inbox dedupes by evidence key, honours its mutes, and never refiles a
// dismissed row, so this is safe to call after every scan.
func (s *Server) mirrorProposalIdeas(pending []skillpropose.Proposal) {
	if len(pending) == 0 {
		return
	}
	ideas, err := s.suggestions()
	if err != nil {
		return
	}
	for _, p := range pending {
		_, _, _ = ideas.Create(proposalIdea(p))
	}
}

// settleProposalIdea closes the mirrored Ideas row when the draft is
// accepted or dismissed in Customize, so the inbox does not keep a card
// for a decision already made.
func (s *Server) settleProposalIdea(id string, dismiss bool) {
	ideas, err := s.suggestions()
	if err != nil {
		return
	}
	list, _, err := ideas.List()
	if err != nil {
		return
	}
	key := proposalIdeaKey(id)
	for _, sg := range list {
		if sg.Source != proposalIdeaSource || sg.EvidenceKey != key {
			continue
		}
		if sg.State != suggestion.StateOpen && sg.State != suggestion.StateSnoozed {
			return
		}
		if dismiss {
			_, _ = ideas.Dismiss(sg.ID, false)
		} else {
			_, _ = ideas.Accept(sg.ID)
		}
		return
	}
}
