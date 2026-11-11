package httpapi

import (
	"easynr/internal/auth"
	"errors"
	"net/http"
)

func (a *server) listTokens(w http.ResponseWriter, r *http.Request) {
	items, err := a.auth.Tokens()
	if err != nil {
		failure(w, 500, "internal_error", "Unable to list tokens")
		return
	}
	reply(w, 200, map[string]any{"items": items})
}
func (a *server) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string       `json:"name"`
		Grants []auth.Grant `json:"grants"`
	}
	if !decode(w, r, &in) {
		return
	}
	token, secret, err := a.auth.CreateToken(in.Name, in.Grants)
	if errors.Is(err, auth.ErrInvalid) {
		failure(w, 400, "invalid_token", "Provide a name and unique package, @scope/* or * read/write grants")
		return
	}
	if err != nil {
		failure(w, 500, "internal_error", "Unable to create token")
		return
	}
	reply(w, 201, map[string]any{"token": token, "secret": secret})
}
func (a *server) revokeToken(w http.ResponseWriter, r *http.Request) {
	err := a.auth.Revoke(r.PathValue("id"))
	if errors.Is(err, auth.ErrNotFound) {
		failure(w, 404, "not_found", "Token does not exist")
		return
	}
	if err != nil {
		failure(w, 500, "internal_error", "Unable to revoke token")
		return
	}
	w.WriteHeader(204)
}
