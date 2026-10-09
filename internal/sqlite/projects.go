package sqlite

import (
	"database/sql"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const projectColumns = `id, name, root_path, integration, regression_json, plan_rev, target_branch, workspace_root, max_attempts, retry_cooldown_ms, created_at, updated_at`

func scanProject(sc interface{ Scan(...any) error }) (project.Project, error) {
	var p project.Project
	var root sql.NullString
	var regression string
	var created, updated, cooldown int64
	if err := sc.Scan(&p.ID, &p.Name, &root, &p.Integration, &regression, &p.PlanRev, &p.TargetBranch, &p.WorkspaceRoot, &p.MaxAttempts, &cooldown, &created, &updated); err != nil {
		return p, err
	}
	p.RootPath = root.String
	p.RetryCooldown = time.Duration(cooldown) * time.Millisecond
	p.CreatedAt, p.UpdatedAt = fromMS(created), fromMS(updated)
	pol, err := verification.Parse([]byte(regression))
	if err != nil {
		return p, wrapInternal(err, "decode project regression policy")
	}
	p.Regression = pol.Regression
	return p, nil
}

// InsertProject stores a new project. A UNIQUE violation on root_path means
// the directory is already registered.
func (t *Tx) InsertProject(p project.Project) error {
	regression, err := (verification.Policy{Regression: p.Regression}).Canonical()
	if err != nil {
		return wrapInternal(err, "encode regression policy")
	}
	var root any
	if p.RootPath != "" {
		root = p.RootPath
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, root, p.Integration, string(regression), p.PlanRev, p.TargetBranch, p.WorkspaceRoot, p.MaxAttempts, p.RetryCooldown.Milliseconds(), ms(p.CreatedAt), ms(p.UpdatedAt))
	if IsUniqueViolation(err) {
		return fault.Wrap(err, fault.CodeInvalidInput, "project id or root path already registered")
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

// GetProjectByRoot loads the project registered at exactly root.
func (t *Tx) GetProjectByRoot(root string) (project.Project, bool, error) {
	row := t.tx.QueryRowContext(t.ctx, `SELECT `+projectColumns+` FROM projects WHERE root_path = ?`, root)
	p, err := scanProject(row)
	if isNoRows(err) {
		return p, false, nil
	}
	return p, err == nil, wrapInternal(err, "get project by root")
}

// GetProjectByName loads a project by display name; ambiguity is an error.
func (t *Tx) GetProjectByName(name string) (project.Project, bool, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+projectColumns+` FROM projects WHERE name = ? LIMIT 2`, name)
	if err != nil {
		return project.Project{}, false, wrapInternal(err, "get project by name")
	}
	defer rows.Close()
	var found []project.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return project.Project{}, false, wrapInternal(err, "scan project")
		}
		found = append(found, p)
	}
	switch len(found) {
	case 0:
		return project.Project{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return project.Project{}, false, fault.New(fault.CodeInvalidInput, "project name %q is ambiguous; use its id", name)
}

// ListProjects returns every project ordered by creation.
func (t *Tx) ListProjects() ([]project.Project, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, wrapInternal(err, "list projects")
	}
	defer rows.Close()
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

// ProjectRoots returns every registered root path for prefix matching.
func (t *Tx) ProjectRoots() (map[string]project.ID, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT root_path, id FROM projects WHERE root_path IS NOT NULL`)
	if err != nil {
		return nil, wrapInternal(err, "list project roots")
	}
	defer rows.Close()
	out := map[string]project.ID{}
	for rows.Next() {
		var root string
		var id project.ID
		if err := rows.Scan(&root, &id); err != nil {
			return nil, wrapInternal(err, "scan root")
		}
		out[root] = id
	}
	return out, rows.Err()
}

// UpdateProject rewrites the mutable fields of a project.
func (t *Tx) UpdateProject(p project.Project, now time.Time) error {
	regression, err := (verification.Policy{Regression: p.Regression}).Canonical()
	if err != nil {
		return wrapInternal(err, "encode regression policy")
	}
	var root any
	if p.RootPath != "" {
		root = p.RootPath
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE projects SET name = ?, root_path = ?, integration = ?, regression_json = ?, target_branch = ?, workspace_root = ?, max_attempts = ?, retry_cooldown_ms = ?, updated_at = ? WHERE id = ?`,
		p.Name, root, p.Integration, string(regression), p.TargetBranch, p.WorkspaceRoot, p.MaxAttempts, p.RetryCooldown.Milliseconds(), ms(now), p.ID)
	if IsUniqueViolation(err) {
		return fault.Wrap(err, fault.CodeInvalidInput, "root path already registered to another project")
	}
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
		return 0, fault.New(fault.CodePlanConflict, "plan revision is %d, change set expected %d; re-read the plan and re-review", current, expected)
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
		return 0, fault.New(fault.CodePlanConflict, "plan revision is %d, change set expected %d; re-read the plan and re-review", current, expected)
	}
	return current, nil
}
