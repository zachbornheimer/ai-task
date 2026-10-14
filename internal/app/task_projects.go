package app

import (
	"slices"

	"github.com/zachbornheimer/ai-task/internal/fault"
	"github.com/zachbornheimer/ai-task/internal/project"
	"github.com/zachbornheimer/ai-task/internal/sqlite"
	"github.com/zachbornheimer/ai-task/internal/task"
)

// requireProjectsExist refuses a membership list naming an unregistered project.
func (a *applier) requireProjectsExist(ids []project.ID) error {
	for _, id := range ids {
		if _, err := a.tx.GetProject(id); err != nil {
			return err
		}
	}
	return nil
}

// replaceProjects sets the ordered project list of a task. The first entry
// is the home; it may change only while the task has never been claimed,
// stays the home of its group and, for a group, of its children.
func (a *applier) replaceProjects(rec sqlite.Record, want []project.ID) (task.Task, bool, error) {
	t := rec.Task
	want = slices.Compact(slices.Clone(want))
	if len(want) == 0 {
		return t, false, fault.New(fault.CodeInvalidInput, "%s: --project needs at least one project", t.ID)
	}
	if err := a.requireProjectsExist(want); err != nil {
		return t, false, err
	}
	if slices.Equal(want, t.Projects) {
		return t, false, nil
	}
	if want[0] != t.ProjectID {
		if err := a.requireHomeMovable(rec, want[0]); err != nil {
			return t, false, err
		}
	}
	if err := a.tx.SetTaskProjects(t.ID, want); err != nil {
		return t, false, err
	}
	t.ProjectID, t.Projects = want[0], want
	return t, true, nil
}

func (a *applier) requireHomeMovable(rec sqlite.Record, home project.ID) error {
	t := rec.Task
	if rec.Attempt != nil {
		return fault.New(fault.CodeInvalidInput, "%s: home project is fixed after the first claim", t.ID)
	}
	if t.ParentID != "" {
		parent, err := a.tx.GetRecord(t.ParentID)
		if err != nil {
			return err
		}
		if parent.Task.ProjectID != home {
			return fault.New(fault.CodeInvalidInput, "%s: a group's children share the group's home project", t.ID)
		}
	}
	children, err := a.tx.Children(t.ID)
	if err != nil {
		return err
	}
	if len(children) > 0 {
		return fault.New(fault.CodeInvalidInput, "%s: a group's children share the group's home project; move the children first", t.ID)
	}
	return nil
}
