package main

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// launchParentError is what selectLaunchParent returns instead of exiting, so
// each call site (launch, add, the capacity pre-check) keeps its own surface.
// UnresolvedID is set only for a caller id that resolves to nothing.
type launchParentError struct {
	Code         string
	Message      string
	UnresolvedID string
}

func (e *launchParentError) Error() string { return e.Message }

// launchParentErrorParts splits a selector error into message and error code.
func launchParentErrorParts(err error) (message, code string) {
	var lpe *launchParentError
	if errors.As(err, &lpe) {
		return lpe.Message, lpe.Code
	}
	return err.Error(), ErrCodeInvalidOperation
}

// selectLaunchParent picks the session a new session is linked to. It is the
// one place launch, add and the capacity pre-check decide this, so they cannot
// drift apart again.
//
// A parent the user names (explicit) is used as is when it is top-level, and
// refused with the single-level error when it is a sub-session. A parent
// agent-deck picks itself (the launching session) is used as is when it is
// top-level. When it is a sub-session, its own parent is used instead, with a
// one-line note: the deck stores one level only, and a session started from a
// sub-session belongs next to it. One hop, never a walk; a parent that is
// missing or itself a sub-session is an error naming both ids.
//
// noParent means top-level and is honoured from every caller. When the caller
// is a sub-session the note says where the session would have landed. A caller
// id that resolves to nothing is an error, except with noParent.
func selectLaunchParent(explicit string, noParent bool, instances []*session.Instance) (*session.Instance, string, error) {
	if explicit != "" {
		named, message, _ := ResolveSession(explicit, instances)
		if named == nil {
			return nil, "", &launchParentError{Code: ErrCodeNotFound, Message: message}
		}
		if named.IsSubSession() {
			return nil, "", &launchParentError{Code: ErrCodeInvalidOperation, Message: "cannot create sub-session of a sub-session (single level only)"}
		}
		return named, "", nil
	}

	caller, unresolved := resolveAutoParentInstanceChecked(instances)
	if noParent {
		if caller == nil || !caller.IsSubSession() {
			return nil, "", nil
		}
		grandparent := findInstanceByID(instances, caller.ParentSessionID)
		if grandparent == nil || grandparent.IsSubSession() {
			return nil, "", nil
		}
		return nil, fmt.Sprintf("-no-parent given from sub-session %s; started top-level instead of under its parent %s", caller.Title, grandparent.Title), nil
	}
	if caller == nil {
		if unresolved != "" {
			return nil, "", &launchParentError{
				Code:         ErrCodeNotFound,
				UnresolvedID: unresolved,
				Message: fmt.Sprintf("automatic parent %q could not be resolved; use --parent with a valid session or --no-parent for an intentional top-level session",
					unresolved),
			}
		}
		return nil, "", nil
	}
	return underParent(caller, instances)
}

// underParent returns inst when it is top-level, otherwise its parent.
func underParent(inst *session.Instance, instances []*session.Instance) (*session.Instance, string, error) {
	if !inst.IsSubSession() {
		return inst, "", nil
	}
	parent := findInstanceByID(instances, inst.ParentSessionID)
	if parent == nil {
		return nil, "", &launchParentError{
			Code:    ErrCodeInvalidOperation,
			Message: fmt.Sprintf("cannot attach to its parent: %s, the parent of sub-session %s, does not exist", inst.ParentSessionID, inst.ID),
		}
	}
	if parent.IsSubSession() {
		return nil, "", &launchParentError{
			Code:    ErrCodeInvalidOperation,
			Message: fmt.Sprintf("cannot attach to its parent: %s, the parent of sub-session %s, is itself a sub-session (single level only)", parent.ID, inst.ID),
		}
	}
	return parent, fmt.Sprintf("parent %s is a sub-session; linked under its parent %s", inst.Title, parent.Title), nil
}

func findInstanceByID(instances []*session.Instance, id string) *session.Instance {
	for _, inst := range instances {
		if inst.ID == id {
			return inst
		}
	}
	return nil
}
