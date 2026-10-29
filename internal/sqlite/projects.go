package sqlite

import (
	"fmt"
	"strings"
	"time"

	"github.com/zachbornheimer/ai-task/internal/discard"
	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
)

const projectColumns = `id, name, plan_rev, max_attempts, retry_cooldown_ms, created_at, updated_at`

func scanProject(sc interface{ Scan(...any) error }) (project.Project, error) {
	var p project.Project
	var created, updated, cooldown int64
	if err := sc.Scan(&p.ID, &p.Name, &p.PlanRev, &p.MaxAttempts, &cooldown, &created, &updated); err != nil {
		return p, err
	}
	p.RetryCooldown = time.Duration(cooldown) * time.Millisecond
	p.CreatedAt, p.UpdatedAt = fromMS(created), fromMS(updated)
	return p, nil
}

// InsertProject stores a new project.
func (t *Tx) InsertProject(p project.Project) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.PlanRev, p.MaxAttempts, p.RetryCooldown.Milliseconds(), ms(p.CreatedAt), ms(p.UpdatedAt))
	if IsUniqueViolation(err) {
		return fault.Wrap(err, fault.CodeInvalidInput, "project id already registered")
	}
	return wrapInternal(err, "insert project")
}

// GetProject loads a project by ID.
func (t *Tx) GetProject(id project.ID) (project.Project, error) {
	row := t.tx.QueryRowContext(t.ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if isNoRows(err) {
		return p, fault.New(fault.CodeNotFound, "project %s not found", id)
	}
	return p, wrapInternal(err, "get project")
}

// GetProjectByName loads a project by display name. Names match when
// project.NameKey is equal, so case and punctuation do not distinguish
// them. Ambiguity is a CodeInvalidInput error that lists each matching
// project's id, so the caller can retry with an id. A name that strips to
// an empty key matches nothing.
func (t *Tx) GetProjectByName(name string) (project.Project, bool, error) {
	key := project.NameKey(name)
	if key == "" {
		return project.Project{}, false, nil
	}
	all, err := t.ListProjects()
	if err != nil {
		return project.Project{}, false, err
	}
	var found []project.Project
	for i := range all {
		if project.NameKey(all[i].Name) == key {
			found = append(found, all[i])
		}
	}
	switch len(found) {
	case 0:
		return project.Project{}, false, nil
	case 1:
		return found[0], true, nil
	}
	matches := make([]string, len(found))
	for i := range found {
		matches[i] = fmt.Sprintf("%s (%s)", found[i].ID, found[i].Name)
	}
	return project.Project{}, false, fault.New(fault.CodeInvalidInput, "project name %q is ambiguous; use its id: %s", name, strings.Join(matches, ", "))
}

// ListProjects returns every project ordered by creation.
func (t *Tx) ListProjects() ([]project.Project, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, wrapInternal(err, "list projects")
	}
	defer discard.CloseReadOnly(rows)
	var out []project.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, wrapInternal(err, "scan project")
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject rewrites the mutable fields of a project.
func (t *Tx) UpdateProject(p project.Project, now time.Time) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE projects SET name = ?, max_attempts = ?, retry_cooldown_ms = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.MaxAttempts, p.RetryCooldown.Milliseconds(), ms(now), p.ID)
	if err != nil {
		return wrapInternal(err, "update project")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fault.New(fault.CodeNotFound, "project %s not found", p.ID)
	}
	return nil
}

// BumpPlanRev advances the plan revision, checking the expected value when
// expected is non-zero. Returns the new revision.
func (t *Tx) BumpPlanRev(pid project.ID, expected uint64, now time.Time) (uint64, error) {
	var current uint64
	if err := t.tx.QueryRowContext(t.ctx, `SELECT plan_rev FROM projects WHERE id = ?`, pid).Scan(&current); err != nil {
		if isNoRows(err) {
			return 0, fault.New(fault.CodeNotFound, "project %s not found", pid)
		}
		return 0, wrapInternal(err, "read plan revision")
	}
	if expected != 0 && expected != current {
		return 0, fault.New(fault.CodeConflict, "plan revision is %d, change set expected %d; re-read the plan and re-review", current, expected)
	}
	next := current + 1
	if _, err := t.tx.ExecContext(t.ctx, `UPDATE projects SET plan_rev = ?, updated_at = ? WHERE id = ?`, next, ms(now), pid); err != nil {
		return 0, wrapInternal(err, "bump plan revision")
	}
	return next, nil
}

// PlanChange looks up a stored idempotent apply result.
func (t *Tx) PlanChange(pid project.ID, key string) (body, digest string, ok bool, err error) {
	err = t.tx.QueryRowContext(t.ctx, `SELECT result_json, request_digest FROM plan_changes WHERE project_id = ? AND idempotency_key = ?`, pid, key).Scan(&body, &digest)
	if isNoRows(err) {
		return "", "", false, nil
	}
	return body, digest, err == nil, wrapInternal(err, "lookup plan change")
}

// RecordPlanChange stores an apply result under its idempotency key,
// together with the digest of the request that produced it.
func (t *Tx) RecordPlanChange(pid project.ID, key, digest string, rev uint64, body string, now time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO plan_changes (project_id, idempotency_key, request_digest, plan_rev, result_json, applied_at) VALUES (?, ?, ?, ?, ?, ?)`, pid, key, digest, rev, body, ms(now))
	return wrapInternal(err, "record plan change")
}

// PlanRev reads the current plan revision and checks an expectation.
func (t *Tx) PlanRev(pid project.ID, expected uint64) (uint64, error) {
	var current uint64
	if err := t.tx.QueryRowContext(t.ctx, `SELECT plan_rev FROM projects WHERE id = ?`, pid).Scan(&current); err != nil {
		if isNoRows(err) {
			return 0, fault.New(fault.CodeNotFound, "project %s not found", pid)
		}
		return 0, wrapInternal(err, "read plan revision")
	}
	if expected != 0 && expected != current {
		return 0, fault.New(fault.CodeConflict, "plan revision is %d, change set expected %d; re-read the plan and re-review", current, expected)
	}
	return current, nil
}
