package handler

import "vocallabs.com/pandora/services/model"

// Perms is what one signed-in admin may do to one target account. It is the single
// source of truth: the API enforces it and the dashboard renders its buttons from it.
//
//	admin       approve pending -> user, promote user -> admin. Nothing else.
//	superadmin  any change among pending/user/admin, activate/deactivate, delete.
//	nobody      can modify the superadmin or themselves.
type Perms struct {
	Roles  []string `json:"roles"`  // roles this actor may move the target to
	Active bool     `json:"active"` // may deactivate / reactivate the target
	Delete bool     `json:"delete"` // may delete the target's account
}

func permsFor(actor, target *model.User) Perms {
	none := Perms{Roles: []string{}}
	if target.Role == model.RoleSuperAdmin || actor.ID == target.ID {
		return none
	}
	switch actor.Role {
	case model.RoleSuperAdmin:
		p := Perms{Roles: []string{}, Active: true, Delete: true}
		for _, r := range []string{model.RolePending, model.RoleUser, model.RoleAdmin} {
			if r != target.Role {
				p.Roles = append(p.Roles, r)
			}
		}
		return p
	case model.RoleAdmin:
		switch target.Role {
		case model.RolePending:
			return Perms{Roles: []string{model.RoleUser}}
		case model.RoleUser:
			return Perms{Roles: []string{model.RoleAdmin}}
		}
	}
	return none
}

func (p Perms) canSetRole(r string) bool {
	for _, x := range p.Roles {
		if x == r {
			return true
		}
	}
	return false
}
