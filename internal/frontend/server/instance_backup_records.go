package server

// Instance backup sections for issues and plans (#1940), plus the
// pre-download size estimate.
//
// Neither store lets a caller choose ids: the issue store numbers issues on
// Create and the plan store mints plan ids. Restore therefore matches on
// content: a record whose text is already on the target is skipped, and
// anything else is created through the store's own Create (then closed or
// approved through its own status path). Text is redacted on export like
// every other section, and the target's text is redacted the same way
// before comparing, so restoring onto the exporting gateway is a no-op.

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/backup"
	"github.com/jonathanung/strike/internal/trust/secret"
)

// backupIssue is one project issue. ID is the source gateway's number and
// is informational only: the target assigns its own.
type backupIssue struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Status string `json:"status"`
}

// backupPlan is one structured plan without its delegate correlation
// (child session progress is runtime state of the source gateway).
type backupPlan struct {
	ID        string              `json:"id"`
	OwnerRoot string              `json:"ownerRoot"`
	Title     string              `json:"title"`
	Status    string              `json:"status"`
	Sections  []backupPlanSection `json:"sections"`
}

type backupPlanSection struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

func (s *Server) backupIssuesService() host.Issues {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Issues
}

func (s *Server) backupPlansService() host.Plans {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Plans
}

// addBackupIssuesAndPlans adds both sections, or names each missing store.
func (s *Server) addBackupIssuesAndPlans(a *backup.Archive) error {
	if iss := s.backupIssuesService(); iss != nil {
		recs, err := exportBackupIssues(iss)
		if err != nil {
			return fmt.Errorf("backup issues: %s", secret.Redact(err.Error()))
		}
		if err := a.Add(backup.SectionIssues, len(recs), recs); err != nil {
			return err
		}
	} else {
		a.Exclude("issues", backup.ExcludeUnavailable, "this gateway has no issue store")
	}
	if pl := s.backupPlansService(); pl != nil {
		recs, err := exportBackupPlans(pl)
		if err != nil {
			return fmt.Errorf("backup plans: %s", secret.Redact(err.Error()))
		}
		if err := a.Add(backup.SectionPlans, len(recs), recs); err != nil {
			return err
		}
	} else {
		a.Exclude("plans", backup.ExcludeUnavailable, "this gateway has no plan store")
	}
	return nil
}

func exportBackupIssues(iss host.Issues) ([]backupIssue, error) {
	list, err := iss.List("")
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]backupIssue, 0, len(list))
	for _, it := range list {
		out = append(out, backupIssue{
			ID:     it.ID,
			Title:  secret.Redact(it.Title),
			Body:   secret.Redact(it.Body),
			Status: normIssueStatus(it.Status),
		})
	}
	return out, nil
}

func exportBackupPlans(pl host.Plans) ([]backupPlan, error) {
	metas, err := pl.List()
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool {
		if !metas[i].CreatedAt.Equal(metas[j].CreatedAt) {
			return metas[i].CreatedAt.Before(metas[j].CreatedAt)
		}
		return metas[i].ID < metas[j].ID
	})
	out := make([]backupPlan, 0, len(metas))
	for _, m := range metas {
		p, ok, err := pl.Get(m.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // deleted between List and Get
		}
		out = append(out, redactBackupPlan(p))
	}
	return out, nil
}

func redactBackupPlan(p host.Plan) backupPlan {
	secs := make([]backupPlanSection, 0, len(p.Sections))
	for _, sec := range p.Sections {
		secs = append(secs, backupPlanSection{Title: secret.Redact(sec.Title), Body: secret.Redact(sec.Body)})
	}
	return backupPlan{
		ID:        p.ID,
		OwnerRoot: p.OwnerRoot,
		Title:     secret.Redact(p.Title),
		Status:    p.Status,
		Sections:  secs,
	}
}

func normIssueStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "open"
	}
	return s
}

// issueContentKey identifies an issue by its text. Status is not part of
// it: the same issue with a different status is the same issue, and the
// target's status is kept.
func issueContentKey(title, body string) string {
	return strings.TrimSpace(title) + "\x00" + strings.TrimSpace(body)
}

// restoreBackupIssues creates every issue whose title and body the target
// lacks. A closed issue is created then closed through the store.
func (s *Server) restoreBackupIssues(rep *backup.Report, recs []backupIssue, apply bool) {
	iss := s.backupIssuesService()
	var have map[string]host.Issue
	var loadErr error
	if iss != nil {
		var list []host.Issue
		list, loadErr = iss.List("")
		have = make(map[string]host.Issue, len(list))
		for _, it := range list {
			have[issueContentKey(secret.Redact(it.Title), secret.Redact(it.Body))] = it
		}
	}
	for _, rec := range recs {
		title := strings.TrimSpace(rec.Title)
		status := normIssueStatus(rec.Status)
		it := backup.Item{Section: backup.SectionIssues, ID: "#" + strconv.Itoa(rec.ID), Label: title}
		fail := func(detail string) {
			it.Action, it.Detail = backup.ActionFail, detail
			rep.Add(it)
		}
		switch {
		case iss == nil:
			fail("this gateway has no issue store")
			continue
		case loadErr != nil:
			fail(secret.Redact(loadErr.Error()))
			continue
		case title == "":
			fail("issue has an empty title")
			continue
		case status != "open" && status != "closed":
			fail(fmt.Sprintf("unknown issue status %q", rec.Status))
			continue
		}
		key := issueContentKey(title, rec.Body)
		if cur, ok := have[key]; ok {
			it.Action, it.Target = backup.ActionSkip, "#"+strconv.Itoa(cur.ID)
			if normIssueStatus(cur.Status) == status {
				it.Detail = "identical"
			} else {
				it.Detail = fmt.Sprintf("already on this gateway; kept its %s status", normIssueStatus(cur.Status))
			}
			rep.Add(it)
			continue
		}
		it.Action = backup.ActionCreate
		created := host.Issue{Title: title, Body: rec.Body, Status: status}
		if apply {
			got, err := iss.Create(title, rec.Body)
			if err != nil {
				fail(secret.Redact(err.Error()))
				continue
			}
			created.ID = got.ID
			it.Target = "#" + strconv.Itoa(got.ID)
			if status == "closed" {
				if _, err := iss.Close(got.ID); err != nil {
					// The issue exists open; say so rather than hide it.
					have[key] = got
					fail(fmt.Sprintf("created as #%d but could not close it: %s", got.ID, secret.Redact(err.Error())))
					continue
				}
			}
		}
		// A repeat later in the same archive skips in both modes, so the
		// dry run predicts the restore.
		have[key] = created
		rep.Add(it)
	}
}

// planContentKey identifies a plan by title and section text.
func planContentKey(p backupPlan) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(p.Title))
	for _, sec := range p.Sections {
		b.WriteString("\x00")
		b.WriteString(strings.TrimSpace(sec.Title))
		b.WriteString("\x01")
		b.WriteString(strings.TrimSpace(sec.Body))
	}
	return b.String()
}

var backupPlanStatuses = map[string]bool{"draft": true, "approved": true, "closed": true}

// restoreBackupPlans creates every plan the target lacks. Matching:
//   - same title and section text under any id: skip;
//   - same id with different content: copy under a " (restored)" title
//     (or skip when an earlier restore already made that copy);
//   - otherwise: create.
//
// New plans start as drafts in the store; an approved or closed plan is
// then moved to that status through the store's own CAS transition.
func (s *Server) restoreBackupPlans(rep *backup.Report, recs []backupPlan, apply bool) {
	pl := s.backupPlansService()
	byID := map[string]backupPlan{}
	byContent := map[string]backupPlan{}
	titles := map[string]bool{}
	var loadErr error
	if pl != nil {
		var metas []host.PlanMeta
		metas, loadErr = pl.List()
		for _, m := range metas {
			if loadErr != nil {
				break
			}
			p, ok, err := pl.Get(m.ID)
			if err != nil {
				loadErr = err
				break
			}
			if !ok {
				continue
			}
			bp := redactBackupPlan(p)
			byID[bp.ID] = bp
			byContent[planContentKey(bp)] = bp
			titles[strings.TrimSpace(p.Title)] = true
		}
	}
	for _, rec := range recs {
		id := strings.TrimSpace(rec.ID)
		title := strings.TrimSpace(rec.Title)
		owner := strings.TrimSpace(rec.OwnerRoot)
		status := strings.ToLower(strings.TrimSpace(rec.Status))
		if status == "" {
			status = "draft"
		}
		it := backup.Item{Section: backup.SectionPlans, ID: id, Label: title}
		fail := func(detail string) {
			it.Action, it.Detail = backup.ActionFail, detail
			rep.Add(it)
		}
		switch {
		case pl == nil:
			fail("this gateway has no plan store")
			continue
		case loadErr != nil:
			fail(secret.Redact(loadErr.Error()))
			continue
		case title == "":
			fail("plan has an empty title")
			continue
		case owner == "":
			fail("plan has no owning root session")
			continue
		case !backupPlanStatuses[status]:
			fail(fmt.Sprintf("unknown plan status %q", rec.Status))
			continue
		}
		rec.Title = title
		key := planContentKey(rec)
		writeTitle := title
		if cur, ok := byContent[key]; ok {
			it.Action, it.Detail = backup.ActionSkip, planSkipDetail(cur.Status, status)
			if cur.ID != id {
				it.Target = cur.ID
			}
			rep.Add(it)
			continue
		}
		if _, ok := byID[id]; ok && id != "" {
			if prev, ok := restoredPlanCopy(rec, titles, byContent); ok {
				it.Action, it.Target, it.Detail = backup.ActionSkip, prev.ID, "already restored as a copy"
				rep.Add(it)
				continue
			}
			writeTitle = backup.CopyName(title, func(t string) bool { return titles[t] })
			it.Action, it.Detail = backup.ActionCopy, fmt.Sprintf("differs from this gateway's plan with the same id; restored as %q", writeTitle)
		} else {
			it.Action = backup.ActionCreate
		}
		newID := ""
		if apply {
			got, err := createBackupPlan(pl, owner, writeTitle, rec.Sections, status)
			if err != nil && got == "" {
				fail(secret.Redact(err.Error()))
				continue
			}
			newID, it.Target = got, got
			if err != nil {
				// The plan exists as a draft; record it so a repeat skips,
				// and report the half-done write instead of hiding it.
				written := rec
				written.ID, written.Title, written.Status = got, writeTitle, "draft"
				titles[writeTitle] = true
				byContent[planContentKey(written)] = written
				fail(fmt.Sprintf("created as %s (draft) but could not set status %s: %s", got, status, secret.Redact(err.Error())))
				continue
			}
		}
		// Later records in the same archive see this write in both modes,
		// so the dry run predicts the same skips and copy titles.
		titles[writeTitle] = true
		written := rec
		written.ID, written.Title, written.Status = newID, writeTitle, status
		byContent[planContentKey(written)] = written
		rep.Add(it)
	}
}

// restoredPlanCopy finds an earlier " (restored)" copy of rec on the
// target, so restoring the same archive twice does not stack copies.
func restoredPlanCopy(rec backupPlan, titles map[string]bool, byContent map[string]backupPlan) (backupPlan, bool) {
	cand := rec
	for n := 1; n < 1000; n++ {
		if n == 1 {
			cand.Title = rec.Title + " (restored)"
		} else {
			cand.Title = fmt.Sprintf("%s (restored %d)", rec.Title, n)
		}
		if !titles[cand.Title] {
			return backupPlan{}, false
		}
		if p, ok := byContent[planContentKey(cand)]; ok {
			return p, true
		}
	}
	return backupPlan{}, false
}

func planSkipDetail(have, archived string) string {
	if have == archived {
		return "identical"
	}
	return fmt.Sprintf("already on this gateway; kept its %s status", have)
}

// createBackupPlan creates the plan and moves it to status. The returned id
// is set whenever the plan was created, even if the status move failed.
func createBackupPlan(pl host.Plans, owner, title string, secs []backupPlanSection, status string) (string, error) {
	in := make([]host.PlanSection, 0, len(secs))
	for _, sec := range secs {
		in = append(in, host.PlanSection{Title: sec.Title, Body: sec.Body})
	}
	p, err := pl.Create(owner, title, in)
	if err != nil {
		return "", err
	}
	if status == "draft" || p.Status == status {
		return p.ID, nil
	}
	if _, err := pl.SetStatus(p.ID, owner, status, p.Version); err != nil {
		return p.ID, err
	}
	return p.ID, nil
}

// backupEstimate is the pre-download size of an export: the manifest of
// the archive the export route would produce right now, without the data.
type backupEstimate struct {
	TotalBytes int                   `json:"totalBytes"`
	Sections   []backup.SectionEntry `json:"sections"`
	Excluded   []backup.Exclusion    `json:"excluded"`
}

// handleBackupEstimate builds the archive exactly as export does and
// reports its size per section. It costs as much as an export (sessions
// are read and redacted) but sends only the manifest.
func (s *Server) handleBackupEstimate(w http.ResponseWriter, r *http.Request) {
	a, err := s.buildBackup(time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	data, err := a.Encode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, backupEstimate{
		TotalBytes: len(data),
		Sections:   append([]backup.SectionEntry{}, a.Manifest.Sections...),
		Excluded:   append([]backup.Exclusion{}, a.Manifest.Excluded...),
	})
}
