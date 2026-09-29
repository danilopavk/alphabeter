package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/danilopavk/alphabeter/internal/clientip"
	"github.com/danilopavk/alphabeter/internal/idp"
	"github.com/danilopavk/alphabeter/internal/ui"
)

type deleteAccountPage struct {
	Error string
}

func (s *Server) showDeleteAccount(w http.ResponseWriter, r *http.Request, userID int64) {
	s.ui.Page(w, http.StatusOK, "delete-account", deleteAccountPage{})
}

// deleteAccount deletes the parent's account with all their kids, once they
// confirm with their password, and logs them out.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, userID int64) {
	err := s.idp.DeleteAccount(r.Context(), userID, r.PostFormValue("password"), clientip.Of(r))
	switch {
	case errors.Is(err, idp.ErrInvalidCredentials):
		s.ui.Page(w, http.StatusUnauthorized, "delete-account", deleteAccountPage{Error: "That password isn't right."})
		return
	case errors.Is(err, idp.ErrRateLimited):
		s.ui.Page(w, http.StatusTooManyRequests, "delete-account",
			deleteAccountPage{Error: "Too many attempts. Try again in a few minutes."})
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	// The refresh tokens went with the account; this ends the access token too.
	if c, err := r.Cookie(accessCookie); err == nil {
		if err := s.idp.Revoke(r.Context(), c.Value); err != nil {
			slog.Error("delete account", "err", err)
		}
	}
	s.clearSession(w)
	s.ui.Notice(w, http.StatusOK, ui.Notice{
		Title:    "Account deleted",
		Message:  "Your account and all your kids' progress are deleted. Our backups drop them within 30 days.",
		LinkHref: "/",
		LinkText: "Back to the start",
	})
}
