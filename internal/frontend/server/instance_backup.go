package server

// Instance backup and restore (#1339): GET /v1/backup/export streams one
// portable archive of this gateway's sessions, memory, schedules and
// non-secret settings; POST /v1/backup/restore validates an archive and
// either reports what it would do (?dryRun=1) or does it.
//
// Restore never overwrites silently: a record the target already has
// byte-for-byte is skipped, a differing one is restored as a copy under a
// " (restored)" name or a fresh id, and every decision is reported. Every
// write goes through the owning store's own API (session.Manager,
// host.Memory, schedule.Service, host.Settings), never raw files.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/backup"
	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/product/version"
	"github.com/jonathanung/strike/internal/schedule"
	"github.com/jonathanung/strike/internal/trust/secret"
)

// maxBackupPayload bounds a restore upload. Sessions dominate the size.
const maxBackupPayload = 256 << 20

// backupMemory is one memory record. Scope is project or global.
type backupMemory struct {
	Scope  string   `json:"scope"`
	Key    string   `json:"key"`
	Value  string   `json:"value"`
	Tags   []string `json:"tags,omitempty"`
	Author string   `json:"author,omitempty"`
}

// backupSchedule is the portable part of a schedule: its definition, not
// its run history, next fire, or per-schedule permission mode.
type backupSchedule struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Enabled     bool      `json:"enabled"`
	Kind        string    `json:"kind"`
	Cron        string    `json:"cron,omitempty"`
	Every       string    `json:"every,omitempty"`
	At          time.Time `json:"at,omitempty"`
	Timezone    string    `json:"timezone,omitempty"`
	Source      string    `json:"source"`
	Prompt      string    `json:"prompt,omitempty"`
	GoalID      string    `json:"goalId,omitempty"`
	Workflow    string    `json:"workflow,omitempty"`
	WorkspaceID string    `json:"workspaceId,omitempty"`
	Overlap     string    `json:"overlap,omitempty"`
	Missed      string    `json:"missed,omitempty"`
}

// backupSettings is the non-secret, non-trust slice of user defaults.
type backupSettings struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Theme    string `json:"theme,omitempty"`
}

// backupAlwaysExcluded is listed in every manifest so the omission is
// auditable. Secrets are never read by the exporter at all.
var backupAlwaysExcluded = []backup.Exclusion{
	{Category: "provider credentials (~/.strike/auth.json, API keys, OAuth tokens)", Kind: backup.ExcludeSecret, Reason: "secrets never leave the gateway; sign in again on the target"},
	{Category: "gateway bearer tokens and pairing codes", Kind: backup.ExcludeSecret, Reason: "a backup must not grant access to the gateway it came from"},
	{Category: "secret references and environment values (secret://env/*)", Kind: backup.ExcludeSecret, Reason: "values live in the host environment, not in Strike"},
	{Category: "credential-shaped text inside sessions, memory and schedule prompts", Kind: backup.ExcludeSecret, Reason: "scrubbed by the same redaction as session export"},
	{Category: "script schedules", Kind: backup.ExcludePolicy, Reason: "commands may embed credentials and must be re-armed on the target"},
	{Category: "permission mode and sandbox dials (global and per schedule)", Kind: backup.ExcludePolicy, Reason: "trust dials are set deliberately on each gateway, never from a file"},
	{Category: "knowledge pins, artifacts, attachments, goals/autopilot state, workspaces and their rules, issues, plans, ledger", Kind: backup.ExcludeLater, Reason: "not covered by archive version 1"},
}

func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backup.Filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	dryRun := truthyQuery(r.URL.Query().Get("dryRun"))
	if !dryRun && !s.requireMutable(w) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBackupPayload))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, opErrorResponse{Error: fmt.Sprintf("backup upload: %v", err)})
		return
	}
	a, err := backup.Decode(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	rep, err := s.restoreBackup(a, !dryRun)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func truthyQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// buildBackup reads every covered store. A store this host does not run is
// listed as unavailable instead of failing the export.
func (s *Server) buildBackup(now time.Time) (*backup.Archive, error) {
	a := backup.New(now, version.Version)
	for _, ex := range backupAlwaysExcluded {
		a.Exclude(ex.Category, ex.Kind, ex.Reason)
	}

	if set := s.backupSettingsService(); set != nil {
		d := set.Defaults()
		rec := backupSettings{
			Provider: strings.TrimSpace(d.Provider),
			Model:    strings.TrimSpace(d.Model),
			Agent:    strings.TrimSpace(d.Agent),
			Effort:   strings.TrimSpace(d.Effort),
			Theme:    strings.TrimSpace(d.Theme),
		}
		if err := a.Add(backup.SectionSettings, 1, []backupSettings{rec}); err != nil {
			return nil, err
		}
	} else {
		a.Exclude("settings", backup.ExcludeUnavailable, "this gateway has no settings store")
	}

	if mem := s.backupMemoryService(); mem != nil {
		recs, err := exportBackupMemory(mem)
		if err != nil {
			return nil, fmt.Errorf("backup memory: %w", err)
		}
		if err := a.Add(backup.SectionMemory, len(recs), recs); err != nil {
			return nil, err
		}
	} else {
		a.Exclude("memory", backup.ExcludeUnavailable, "this gateway has no memory store")
	}

	if s.opts.Schedules != nil {
		recs := exportBackupSchedules(s.opts.Schedules.List())
		if err := a.Add(backup.SectionSchedules, len(recs), recs); err != nil {
			return nil, err
		}
	} else {
		a.Exclude("schedules", backup.ExcludeUnavailable, "this gateway runs no schedule clock")
	}

	pkgs, unreadable, err := exportBackupSessions(s.opts.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("backup sessions: %w", err)
	}
	for _, msg := range unreadable {
		a.Exclude(msg, backup.ExcludeUnavailable, "the session log could not be read; it is not in this backup")
	}
	if err := a.Add(backup.SectionSessions, len(pkgs), pkgs); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Server) backupSettingsService() host.Settings {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Settings
}

func (s *Server) backupMemoryService() host.Memory {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Memory
}

func exportBackupMemory(mem host.Memory) ([]backupMemory, error) {
	var out []backupMemory
	add := func(scope string, entries []host.MemoryEntry) {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		for _, e := range entries {
			out = append(out, backupMemory{
				Scope:  scope,
				Key:    e.Key,
				Value:  secret.Redact(e.Value),
				Tags:   append([]string(nil), e.Tags...),
				Author: e.Author,
			})
		}
	}
	if scoped, ok := mem.(host.MemoryScopes); ok {
		for _, scope := range []string{"project", "global"} {
			entries, err := scoped.ListScope(scope, "")
			if err != nil {
				return nil, err
			}
			add(scope, entries)
		}
	} else {
		entries, err := mem.List("")
		if err != nil {
			return nil, err
		}
		add("project", entries)
	}
	if out == nil {
		out = []backupMemory{}
	}
	return out, nil
}

func exportBackupSchedules(list []schedule.Schedule) []backupSchedule {
	out := []backupSchedule{}
	for _, sch := range list {
		if sch.Source == schedule.SourceScript {
			continue
		}
		out = append(out, backupSchedule{
			ID:          sch.ID,
			Name:        secret.Redact(sch.Name),
			Enabled:     sch.Enabled,
			Kind:        sch.Kind,
			Cron:        sch.Cron,
			Every:       sch.Every,
			At:          sch.At,
			Timezone:    sch.Timezone,
			Source:      sch.Source,
			Prompt:      secret.Redact(sch.Prompt),
			GoalID:      sch.GoalID,
			Workflow:    sch.Workflow,
			WorkspaceID: sch.WorkspaceID,
			Overlap:     sch.Overlap,
			Missed:      sch.Missed,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// exportBackupSessions packages every session log on disk, redacted the
// same way as a single-session export. Children travel with their links.
// One unreadable log must not make the whole gateway unbackupable: it is
// returned in unreadable (for the manifest) and the rest are exported.
func exportBackupSessions(dir string) (pkgs []session.Package, unreadable []string, err error) {
	infos, err := session.NewManager(dir).List()
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	pkgs = make([]session.Package, 0, len(infos))
	for _, info := range infos {
		pkg, err := session.ExportPackage(dir, info.ID)
		if err != nil {
			unreadable = append(unreadable, "session "+info.ID+": "+secret.Redact(err.Error()))
			continue
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, unreadable, nil
}

// restoreBackup plans (apply=false) or performs the restore. Sections run
// in backup.KnownSections order; one record's failure is reported and the
// rest continue, so a partial target is always described by the report.
func (s *Server) restoreBackup(a *backup.Archive, apply bool) (*backup.Report, error) {
	rep := backup.NewReport(a, !apply)
	var settings []backupSettings
	if ok, err := a.Section(backup.SectionSettings, &settings); err != nil {
		return nil, err
	} else if ok {
		s.restoreBackupSettings(rep, settings, apply)
	}
	var mem []backupMemory
	if ok, err := a.Section(backup.SectionMemory, &mem); err != nil {
		return nil, err
	} else if ok {
		s.restoreBackupMemory(rep, mem, apply)
	}
	var schedules []backupSchedule
	if ok, err := a.Section(backup.SectionSchedules, &schedules); err != nil {
		return nil, err
	} else if ok {
		s.restoreBackupSchedules(rep, schedules, apply)
	}
	var pkgs []session.Package
	if ok, err := a.Section(backup.SectionSessions, &pkgs); err != nil {
		return nil, err
	} else if ok {
		restoreBackupSessions(rep, s.opts.SessionDir, pkgs, apply)
	}
	return rep, nil
}

func (s *Server) restoreBackupSettings(rep *backup.Report, recs []backupSettings, apply bool) {
	set := s.backupSettingsService()
	if len(recs) == 0 {
		return
	}
	rec := recs[0]
	if set == nil {
		rep.Add(backup.Item{Section: backup.SectionSettings, ID: "defaults", Action: backup.ActionFail, Detail: "this gateway has no settings store"})
		return
	}
	cur := set.Defaults()
	fields := []struct {
		id, archived, current string
	}{
		{"provider", rec.Provider, cur.Provider},
		{"model", rec.Model, cur.Model},
		{"agent", rec.Agent, cur.Agent},
		{"effort", rec.Effort, cur.Effort},
		{"theme", rec.Theme, cur.Theme},
	}
	write := map[string]string{}
	for _, f := range fields {
		archived, current := strings.TrimSpace(f.archived), strings.TrimSpace(f.current)
		switch {
		case archived == "":
			continue
		case archived == current:
			rep.Add(backup.Item{Section: backup.SectionSettings, ID: f.id, Label: archived, Action: backup.ActionSkip, Detail: "identical"})
		case current != "":
			rep.Add(backup.Item{Section: backup.SectionSettings, ID: f.id, Label: archived, Action: backup.ActionSkip,
				Detail: fmt.Sprintf("kept this gateway's %q", current)})
		default:
			write[f.id] = archived
		}
	}
	var saveErr error
	if apply && len(write) > 0 {
		if write["provider"] != "" || write["model"] != "" || write["agent"] != "" || write["effort"] != "" {
			saveErr = set.SaveDefaults(write["provider"], write["model"], write["agent"], write["effort"], "")
		}
		if saveErr == nil && write["theme"] != "" {
			saveErr = set.SaveTheme(write["theme"])
		}
	}
	for _, f := range fields {
		v, ok := write[f.id]
		if !ok {
			continue
		}
		it := backup.Item{Section: backup.SectionSettings, ID: f.id, Label: v, Action: backup.ActionCreate}
		if saveErr != nil {
			it.Action, it.Detail = backup.ActionFail, saveErr.Error()
		}
		rep.Add(it)
	}
}

func (s *Server) restoreBackupMemory(rep *backup.Report, recs []backupMemory, apply bool) {
	mem := s.backupMemoryService()
	scoped, hasScopes := mem.(host.MemoryScopes)
	existing := map[string]map[string]host.MemoryEntry{}
	load := func(scope string) (map[string]host.MemoryEntry, error) {
		if m, ok := existing[scope]; ok {
			return m, nil
		}
		var entries []host.MemoryEntry
		var err error
		if hasScopes {
			entries, err = scoped.ListScope(scope, "")
		} else {
			entries, err = mem.List("")
		}
		if err != nil {
			return nil, err
		}
		m := make(map[string]host.MemoryEntry, len(entries))
		for _, e := range entries {
			m[e.Key] = e
		}
		existing[scope] = m
		return m, nil
	}
	put := func(scope, key string, rec backupMemory) error {
		if hasScopes {
			_, err := scoped.PutScope(scope, key, rec.Value, rec.Author, rec.Tags)
			return err
		}
		return mem.Put(key, rec.Value, rec.Tags)
	}
	for _, rec := range recs {
		scope := strings.TrimSpace(rec.Scope)
		if scope == "" {
			scope = "project"
		}
		it := backup.Item{Section: backup.SectionMemory, ID: scope + ":" + rec.Key, Label: rec.Key}
		switch {
		case mem == nil:
			it.Action, it.Detail = backup.ActionFail, "this gateway has no memory store"
			rep.Add(it)
			continue
		case !hasScopes && scope != "project":
			it.Action, it.Detail = backup.ActionFail, "this gateway has no "+scope+" memory scope"
			rep.Add(it)
			continue
		}
		cur, err := load(scope)
		if err != nil {
			it.Action, it.Detail = backup.ActionFail, err.Error()
			rep.Add(it)
			continue
		}
		key := rec.Key
		if have, ok := cur[rec.Key]; ok {
			if have.Value == rec.Value {
				it.Action, it.Detail = backup.ActionSkip, "identical"
				rep.Add(it)
				continue
			}
			key = backup.CopyName(rec.Key, func(k string) bool { _, taken := cur[k]; return taken })
			it.Action, it.Target, it.Detail = backup.ActionCopy, key, "differs from this gateway's entry"
		} else {
			it.Action = backup.ActionCreate
		}
		if apply {
			if err := put(scope, key, rec); err != nil {
				it.Action, it.Detail = backup.ActionFail, err.Error()
				rep.Add(it)
				continue
			}
		}
		// Later records in the same archive see this write in both modes,
		// so the dry run predicts the same copy names the restore uses.
		cur[key] = host.MemoryEntry{Key: key, Value: rec.Value}
		rep.Add(it)
	}
}

func (s *Server) restoreBackupSchedules(rep *backup.Report, recs []backupSchedule, apply bool) {
	svc := s.opts.Schedules
	var current []schedule.Schedule
	if svc != nil {
		current = svc.List()
	}
	names := map[string]bool{}
	for _, sch := range current {
		names[sch.Name] = true
	}
	for _, rec := range recs {
		it := backup.Item{Section: backup.SectionSchedules, ID: rec.ID, Label: rec.Name}
		if svc == nil {
			it.Action, it.Detail = backup.ActionFail, "this gateway runs no schedule clock"
			rep.Add(it)
			continue
		}
		if rec.Source == schedule.SourceScript {
			it.Action, it.Detail = backup.ActionFail, "script schedules are never restored from a backup"
			rep.Add(it)
			continue
		}
		if sameScheduleExists(current, rec) {
			it.Action, it.Detail = backup.ActionSkip, "identical"
			rep.Add(it)
			continue
		}
		name := rec.Name
		notes := []string{"restored paused; enable it on this gateway"}
		if names[name] {
			name = backup.CopyName(rec.Name, func(n string) bool { return names[n] })
			it.Action, it.Target = backup.ActionCopy, name
		} else {
			it.Action = backup.ActionCreate
		}
		workspaceID := strings.TrimSpace(rec.WorkspaceID)
		if workspaceID != "" && (s.opts.Workspaces == nil || !workspaceKnown(s, workspaceID)) {
			notes = append(notes, "workspace "+workspaceID+" is not on this gateway; runs in the default directory")
			workspaceID = ""
		}
		it.Detail = strings.Join(notes, "; ")
		if apply {
			off := false
			_, err := svc.Create(schedule.Spec{
				Name:        name,
				Kind:        rec.Kind,
				Cron:        rec.Cron,
				Every:       rec.Every,
				At:          rec.At,
				Timezone:    rec.Timezone,
				Source:      rec.Source,
				Prompt:      rec.Prompt,
				GoalID:      rec.GoalID,
				Workflow:    rec.Workflow,
				WorkspaceID: workspaceID,
				Overlap:     rec.Overlap,
				Missed:      rec.Missed,
				Enabled:     &off,
			})
			if err != nil {
				it.Action, it.Target, it.Detail = backup.ActionFail, "", err.Error()
				rep.Add(it)
				continue
			}
		}
		names[name] = true
		rep.Add(it)
	}
}

func workspaceKnown(s *Server, id string) bool {
	_, ok := s.opts.Workspaces.Get(id)
	return ok
}

// sameScheduleExists reports a target schedule with the same definition
// (name, clock and source). Enabled state and run history do not count.
func sameScheduleExists(current []schedule.Schedule, rec backupSchedule) bool {
	for _, sch := range current {
		if sch.Name == rec.Name && sch.Kind == rec.Kind && sch.Cron == rec.Cron &&
			sch.Every == rec.Every && sch.At.Equal(rec.At) && sch.Source == rec.Source &&
			sch.Prompt == rec.Prompt && sch.GoalID == rec.GoalID && sch.Workflow == rec.Workflow &&
			(rec.Timezone == "" || sch.Timezone == rec.Timezone) {
			return true
		}
	}
	return false
}

func restoreBackupSessions(rep *backup.Report, dir string, pkgs []session.Package, apply bool) {
	mgr := session.NewManager(dir)
	for _, pkg := range pkgs {
		id := strings.TrimSpace(pkg.SourceID)
		it := backup.Item{Section: backup.SectionSessions, ID: id, Label: strings.TrimSpace(pkg.Meta.Title)}
		if err := session.ValidatePackage(pkg); err != nil {
			it.Action, it.Detail = backup.ActionFail, err.Error()
			rep.Add(it)
			continue
		}
		exists, err := sessionLogExists(dir, id)
		if err != nil {
			it.Action, it.Detail = backup.ActionFail, err.Error()
			rep.Add(it)
			continue
		}
		if exists {
			have, err := session.ExportPackage(dir, id)
			if err == nil && session.SameEvents(have, pkg) {
				it.Action, it.Detail = backup.ActionSkip, "identical"
				rep.Add(it)
				continue
			}
			base := it.Label
			if base == "" {
				base = id
			}
			title := base + " (restored)"
			it.Action, it.Detail = backup.ActionCopy, "differs from this gateway's session; restored under a new id"
			if apply {
				info, err := mgr.ImportPackageTitled(pkg, title)
				if err != nil {
					it.Action, it.Detail = backup.ActionFail, err.Error()
					rep.Add(it)
					continue
				}
				it.Target = info.ID
			}
			rep.Add(it)
			continue
		}
		it.Action = backup.ActionCreate
		if apply {
			if _, err := mgr.RestorePackage(pkg); err != nil {
				it.Action, it.Detail = backup.ActionFail, err.Error()
			}
		}
		rep.Add(it)
	}
}

// sessionLogExists reports whether id already has a log under dir. An id
// that is not a valid single path segment is an error, never a path.
func sessionLogExists(dir, id string) (bool, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false, fmt.Errorf("session id %q is invalid", id)
	}
	_, err := os.Stat(session.LogPath(dir, id))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
