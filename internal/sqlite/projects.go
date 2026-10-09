package sqlite

import (
	"database/sql"
	"time"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/verification"
)

const projectColumns = `id, name, root_path, integration, regression_json, created_at, updated_at`

func scanProject(sc interface{ Scan(...any) error }) (project.Project, error) {
	var p project.Project
	var root sql.NullString
	var regression string
	var created, updated int64
	if err := sc.Scan(&p.ID, &p.Name, &root, &p.Integration, &regression, &created, &updated); err != nil {
		return p, err
	}
	p.RootPath = root.String
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
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, root, p.Integration, string(regression), ms(p.CreatedAt), ms(p.UpdatedAt))
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
	res, err := t.tx.ExecContext(t.ctx, `UPDATE projects SET name = ?, root_path = ?, integration = ?, regression_json = ?, updated_at = ? WHERE id = ?`,
		p.Name, root, p.Integration, string(regression), ms(now), p.ID)
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
