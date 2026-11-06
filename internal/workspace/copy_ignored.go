package workspace

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

// nonCopyable names entries never copied from the source worktree: VCS
// metadata and worktree-local state a second checkout must not inherit.
var nonCopyable = map[string]bool{
	".git":       true,
	".worktrees": true,
	".DS_Store":  true,
	".idea":      true,
	".vscode":    true,
	".hg":        true,
	".svn":       true,
}

// CopyIgnored copies the gitignored files of the src worktree (typically the
// primary checkout, where dependencies were installed) into dst (a fresh
// task worktree or verify snapshot), so checks do not fail with "cannot find
// module" on a tree that was just created. It replicates the behavior of
// Worktrunk's `wt step copy-ignored`, which Worktrunk task worktrees get via
// their post-start hook but atask's snapshots and hooks-off switches do not.
//
// The copy is idempotent: a file that already exists in dst is left alone,
// so repeated runs never clobber work done in the destination. VCS
// metadata (.git at any depth, including a nested worktree's .git file) and
// worktree-local state (.worktrees) are excluded. An empty ignore set makes
// it a no-op. Errors are returned, never fatal to the caller: provisioning
// is advisory, so callers log and continue.
func CopyIgnored(ctx context.Context, src, dst string) error {
	out, err := git(ctx, src, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return fault.Wrap(err, fault.CodeNotReady, "list ignored files in %s", src)
	}
	for entry := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if err := ctx.Err(); err != nil {
			return fault.Wrap(err, fault.CodeNotReady, "copy ignored files")
		}
		if entry == "" {
			continue
		}
		if nonCopyable[entry] || nonCopyable[filepath.Base(entry)] {
			continue
		}
		from := filepath.Join(src, filepath.FromSlash(entry))
		to := filepath.Join(dst, filepath.FromSlash(entry))
		if strings.HasSuffix(entry, "/") {
			if err := copyTree(ctx, from, to); err != nil {
				return fault.Wrap(err, fault.CodeNotReady, "copy ignored directory %s", entry)
			}
			continue
		}
		if err := copyFile(from, to); err != nil {
			return fault.Wrap(err, fault.CodeNotReady, "copy ignored file %s", entry)
		}
	}
	return nil
}

// copyTree copies the tree rooted at from into to, skipping files that
// already exist in to (idempotency) and every nonCopyable member at any
// depth, including a nested checkout's .git directory or worktree file.
func copyTree(ctx context.Context, from, to string) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if nonCopyable[d.Name()] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			return copySymlink(path, target)
		default:
			return copyFile(path, target)
		}
	})
}

// copyFile copies a regular file when target does not exist yet.
func copyFile(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return nil // already provisioned; never overwrite
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	in, err := os.Open(filepath.Clean(from))
	if err != nil {
		// A file the walk listed can disappear while a dev server rewrites
		// build output; that is a race, not a broken copy.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Clean(to), os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// copySymlink recreates a symlink in to, pointing at the same target,
// without following it.
func copySymlink(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	target, err := os.Readlink(from)
	if err != nil {
		return err
	}
	return os.Symlink(target, to)
}
