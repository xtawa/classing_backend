package httpapi

import (
	"net/http"
	"strings"
)

func (s *Server) membershipStatus(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Membership(r.Context(), principal(r).User.ID)
	if err != nil {
		writeStoreError(w, r, err, "MEMBERSHIP")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"membership": membershipPayload(item)})
}

func (s *Server) redeemMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.Redeem(r.Context(), principal(r).User.ID, strings.TrimSpace(body.Code))
	if err != nil {
		writeStoreError(w, r, err, "MEMBERSHIP_REDEEM")
		return
	}
	s.audit(r, principal(r).User.ID, "MEMBERSHIP_REDEEM", "MEMBERSHIP", principal(r).User.ID, map[string]any{"codePrefix": prefix(body.Code, 7)})
	writeJSON(w, http.StatusOK, map[string]any{"membership": membershipPayload(item)})
}

func prefix(value string, length int) string {
	value = strings.TrimSpace(value)
	if len(value) <= length {
		return value
	}
	return value[:length]
}
