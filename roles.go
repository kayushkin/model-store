package modelstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// Roles are global, purpose-named pointers into the model registry. They exist
// so a caller can ask for "the efficient model" rather than naming a concrete
// id it will then have to keep up to date. A role is NOT tied to a provider or
// a harness: it names a capability tier that any harness can be pointed at,
// which is the whole reason the resolution lives here in the one source of
// truth rather than being reinvented per caller.
//
//   best      — highest performance, cost no object
//   default   — the everyday model: much cheaper than best, better than efficient
//   efficient — cheap enough to run at volume (titles, classification, polling)
//
// The set is fixed and defined ONCE here. Nothing else may carry its own copy;
// a role outside this set is rejected at write time so a typo cannot create a
// silent fourth role that nothing resolves.
const (
	RoleBest      = "best"
	RoleDefault   = "default"
	RoleEfficient = "efficient"
)

// CanonicalRoles is the single definition of the valid role set. Iterate this
// rather than hardcoding the three names anywhere else.
var CanonicalRoles = []string{RoleBest, RoleDefault, RoleEfficient}

// ErrUnknownRole is returned when a role name is not in CanonicalRoles.
var ErrUnknownRole = errors.New("unknown role")

// IsCanonicalRole reports whether name is one of the fixed roles.
func IsCanonicalRole(name string) bool {
	for _, r := range CanonicalRoles {
		if r == name {
			return true
		}
	}
	return false
}

// Roles returns the current role→model-id mapping. A role with no assignment is
// simply absent from the map rather than present with an empty value, so the
// caller can tell "unset" from "set to something".
func (s *Store) Roles() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT role, model_id FROM model_roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var role, modelID string
		if err := rows.Scan(&role, &modelID); err != nil {
			return nil, err
		}
		out[role] = modelID
	}
	return out, rows.Err()
}

// SetRole points a role at a model. The role must be canonical and the model
// must already exist in the registry — a role can only ever name a real model,
// never a free-text string, so it cannot drift out of sync with the models
// table the way a hardcoded default in a caller does.
func (s *Store) SetRole(role, modelID string) error {
	if !IsCanonicalRole(role) {
		return fmt.Errorf("%w: %q (valid roles: %v)", ErrUnknownRole, role, CanonicalRoles)
	}
	var exists string
	err := s.db.QueryRow(`SELECT id FROM models WHERE id = ?`, modelID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cannot point role %q at unknown model %q", role, modelID)
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO model_roles (role, model_id) VALUES (?, ?)
		 ON CONFLICT(role) DO UPDATE SET model_id = excluded.model_id`,
		role, modelID,
	)
	return err
}

// RemoveRole clears a role assignment. Removing a role that is not set is not an
// error — the desired end state (no assignment) already holds.
func (s *Store) RemoveRole(role string) error {
	_, err := s.db.Exec(`DELETE FROM model_roles WHERE role = ?`, role)
	return err
}

// ResolveRole resolves a role to its full model. It fails loud on an unknown
// role name and on a role that has no assignment — a caller asking for "the
// efficient model" when none is set must hear about it, not be handed a
// fabricated fallback that masks the missing configuration.
func (s *Store) ResolveRole(role string) (*Model, error) {
	if !IsCanonicalRole(role) {
		return nil, fmt.Errorf("%w: %q (valid roles: %v)", ErrUnknownRole, role, CanonicalRoles)
	}
	var modelID string
	err := s.db.QueryRow(`SELECT model_id FROM model_roles WHERE role = ?`, role).Scan(&modelID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("role %q is not assigned", role)
	}
	if err != nil {
		return nil, err
	}
	return s.ResolveModel(modelID)
}
