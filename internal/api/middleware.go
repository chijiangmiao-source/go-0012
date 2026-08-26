package api

import (
	"net/http"

	"offshore-buoy-drift-search-loop/internal/domain"
)

func actorFromRequest(r *http.Request) domain.Actor {
	return domain.Actor{
		ID:   r.Header.Get("X-Actor-ID"),
		Role: domain.Role(r.Header.Get("X-Role")),
	}
}

func requireRole(w http.ResponseWriter, r *http.Request, action string) (domain.Actor, bool) {
	actor := actorFromRequest(r)
	if actor.ID == "" || !actor.Role.Can(action) {
		writeError(w, r, domain.NewError(domain.CodeForbidden, "actor is not allowed to perform this action"))
		return actor, false
	}
	return actor, true
}
