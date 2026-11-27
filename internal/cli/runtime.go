package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zachbornheimer/ai-task/internal/workspace"
)

const (
	runtimeRootEnv           = "ATASK_RUNTIME_ROOT"
	runtimeSessionEnv        = "PI_SESSION_ID"
	runtimeSecretBytes       = 32
	runtimeRegistrationLimit = 4096
	runtimeProcessTimeout    = 2 * time.Second
	runtimeAncestorLimit     = 256
)

// runtimeRegistration is the private host-to-CLI protocol. Capability never
// crosses into the engine or its persisted workspace binding.
type runtimeRegistration struct {
	Session     string `json:"session"`
	PID         int    `json:"pid"`
	Incarnation string `json:"incarnation"`
	Capability  string `json:"capability"`
}

func lookupRuntime(ctx context.Context, getenv func(string) string) (*workspace.RuntimeIdentity, error) {
	rootPath, session := getenv(runtimeRootEnv), getenv(runtimeSessionEnv)
	if rootPath == "" || session == "" {
		return nil, nil
	}
	if !filepath.IsAbs(rootPath) || rootPath != filepath.Clean(rootPath) {
		return nil, fmt.Errorf("runtime registry root must be an absolute canonical path")
	}
	rootInfo, err := os.Lstat(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect runtime registry root: %w", err)
	}
	if err := privateRuntimePath(rootInfo, true); err != nil {
		return nil, fmt.Errorf("unsafe runtime registry root: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open runtime registry root: %w", err)
	}
	defer root.Close()
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedInfo) {
		return nil, fmt.Errorf("runtime registry root changed while opening")
	}
	if err := privateRuntimePath(openedInfo, true); err != nil {
		return nil, fmt.Errorf("unsafe opened runtime registry root: %w", err)
	}
	digest := sha256.Sum256([]byte(session))
	name := hex.EncodeToString(digest[:]) + ".json"
	registration, err := readRuntimeRegistration(root, name)
	if err != nil || registration == nil {
		return nil, err
	}
	return registration.identity(ctx, session)
}

func readRuntimeRegistration(root *os.Root, name string) (*runtimeRegistration, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect runtime registration: %w", err)
	}
	if err := privateRuntimePath(info, false); err != nil {
		return nil, fmt.Errorf("unsafe runtime registration: %w", err)
	}
	file, err := openRuntimeRegistration(root, name)
	if err != nil {
		return nil, fmt.Errorf("open runtime registration: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("runtime registration changed while opening")
	}
	if err := privateRuntimePath(openedInfo, false); err != nil {
		return nil, fmt.Errorf("unsafe opened runtime registration: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, runtimeRegistrationLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read runtime registration: %w", err)
	}
	if len(raw) > runtimeRegistrationLimit {
		return nil, fmt.Errorf("runtime registration exceeds %d bytes", runtimeRegistrationLimit)
	}
	var registration runtimeRegistration
	if err := json.Unmarshal(raw, &registration); err != nil {
		// Decoder diagnostics can contain pieces of the private capability.
		return nil, fmt.Errorf("runtime registration is not valid JSON")
	}
	return &registration, nil
}

func (r runtimeRegistration) identity(ctx context.Context, session string) (*workspace.RuntimeIdentity, error) {
	if r.Session != session {
		return nil, fmt.Errorf("runtime registration session does not match current session")
	}
	capability, err := hex.DecodeString(r.Capability)
	if err != nil || len(capability) != runtimeSecretBytes {
		return nil, fmt.Errorf("runtime registration capability must be %d random bytes encoded as hex", runtimeSecretBytes)
	}
	digest := sha256.Sum256(capability)
	identity := &workspace.RuntimeIdentity{Session: r.Session, PID: r.PID, Incarnation: r.Incarnation, CapabilityDigest: hex.EncodeToString(digest[:])}
	if !identity.Valid() {
		return nil, fmt.Errorf("runtime registration has an invalid PID or incarnation")
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeProcessTimeout)
	defer cancel()
	if err := runtimeAncestor(ctx, r.PID); err != nil {
		return nil, fmt.Errorf("runtime registration owner is not a live CLI ancestor: %w", err)
	}
	return identity, nil
}

func runtimeAncestor(ctx context.Context, owner int) error {
	pid := os.Getppid()
	for depth := 0; pid > 0 && depth < runtimeAncestorLimit; depth++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("inspect process ancestry: %w", err)
		}
		parent, err := runtimeParent(ctx, pid)
		if err != nil {
			return err
		}
		if pid == owner {
			return nil
		}
		if parent == pid {
			break
		}
		pid = parent
	}
	return fmt.Errorf("registered PID %d is not in the CLI parent chain", owner)
}
