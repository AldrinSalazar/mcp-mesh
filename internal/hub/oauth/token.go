package oauth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func tokenError(w http.ResponseWriter, e *oauthErr) {
	status := e.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, map[string]string{"error": e.Code, "error_description": e.Desc})
}

var errGrant = func(desc string) *oauthErr { return &oauthErr{Code: "invalid_grant", Desc: desc} }

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		tokenError(w, &oauthErr{Code: "invalid_request", Desc: "the body must be a form under 64 KiB"})
		return
	}
	f := r.PostForm
	if f.Get("client_secret") != "" {
		tokenError(w, &oauthErr{Code: "invalid_client", Desc: "only public clients are supported", Status: http.StatusUnauthorized})
		return
	}
	clientID := f.Get("client_id")
	if clientID == "" {
		tokenError(w, &oauthErr{Code: "invalid_request", Desc: "client_id is required"})
		return
	}
	if res := f.Get("resource"); res != "" && res != s.opts.Resource {
		tokenError(w, &oauthErr{Code: "invalid_target", Desc: "resource must be " + s.opts.Resource})
		return
	}
	var (
		resp *tokenResponse
		oerr *oauthErr
	)
	switch f.Get("grant_type") {
	case "authorization_code":
		resp, oerr = s.exchangeCode(r, clientID)
	case "refresh_token":
		resp, oerr = s.refresh(r, clientID)
	case "":
		oerr = &oauthErr{Code: "invalid_request", Desc: "grant_type is required"}
	default:
		oerr = &oauthErr{Code: "unsupported_grant_type", Desc: "use authorization_code or refresh_token"}
	}
	if oerr != nil {
		tokenError(w, oerr)
		return
	}
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, resp)
}

// issue adds an access and a refresh token for the key, in the grant's
// family.
func (s *Server) issue(tx *sql.Tx, keyID, family string) (*tokenResponse, error) {
	access, err := randomToken()
	if err != nil {
		return nil, err
	}
	refresh, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, t := range []struct {
		tok, kind string
		ttl       time.Duration
	}{{access, "access", s.opts.AccessTTL}, {refresh, "refresh", s.opts.RefreshTTL}} {
		if _, err := tx.Exec("INSERT INTO oauth_tokens (hash, key_id, kind, created_at, expires_at, family) VALUES (?, ?, ?, ?, ?, ?)",
			auth.Hash(t.tok), keyID, t.kind, store.Millis(now), store.Millis(now.Add(t.ttl)), family); err != nil {
			return nil, err
		}
	}
	return &tokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(s.opts.AccessTTL / time.Second),
		RefreshToken: refresh, Scope: scopeMCP,
	}, nil
}

// replayed marks a code whose replay came before its exchange finished, so
// the exchange issues nothing.
const replayed = "replayed"

// grant is what a valid code was approved for.
type grant struct {
	keyID      string // existing key to reuse; "" = make one
	keyName    string
	groups     []string
	approvedBy string
	approvedIP string
	clientName string
}

func (s *Server) exchangeCode(r *http.Request, clientID string) (*tokenResponse, *oauthErr) {
	f := r.PostForm
	code, verifier := f.Get("code"), f.Get("code_verifier")
	if code == "" || verifier == "" || f.Get("redirect_uri") == "" {
		return nil, &oauthErr{Code: "invalid_request", Desc: "code, redirect_uri and code_verifier are required"}
	}
	serverErr := func(err error) (*tokenResponse, *oauthErr) {
		s.log.Error("exchanging code failed", "error", err)
		return nil, &oauthErr{Code: "server_error", Desc: "could not issue tokens", Status: http.StatusInternalServerError}
	}
	hash := auth.Hash(code)
	var (
		g    grant
		oerr *oauthErr
	)
	// First the code is checked and used up, in one transaction; the key (if
	// new) is made outside it, through the key store; then the tokens.
	err := s.opts.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var (
			cid, redirect, challenge, resource, groups string
			keyID, family                              sql.NullString
			exp                                        int64
		)
		err := tx.QueryRow(`SELECT client_id, redirect_uri, code_challenge, resource, expires_at, key_id, key_name, groups, approved_by, approved_ip, family
			FROM oauth_codes WHERE hash = ?`, hash).
			Scan(&cid, &redirect, &challenge, &resource, &exp, &keyID, &g.keyName, &groups, &g.approvedBy, &g.approvedIP, &family)
		if errors.Is(err, sql.ErrNoRows) {
			oerr = errGrant("unknown or expired code")
			return nil
		}
		if err != nil {
			return err
		}
		now := store.Millis(s.now())
		if exp < 0 {
			// Replay of a used code: assume it leaked and cut off what it
			// bought, or what it is about to buy.
			if -exp > now {
				switch {
				case !family.Valid:
					_, err = tx.Exec("UPDATE oauth_codes SET family = ? WHERE hash = ?", replayed, hash)
				case family.String != replayed:
					_, err = tx.Exec("DELETE FROM oauth_tokens WHERE family = ?", family.String)
				}
				if err != nil {
					return err
				}
			}
			oerr = errGrant("code already used")
			return nil
		}
		// Used up by any attempt, right or wrong: a guessed verifier gets
		// one try. The negative expiry keeps the row for replay detection.
		if _, err := tx.Exec("UPDATE oauth_codes SET expires_at = ? WHERE hash = ?", -exp, hash); err != nil {
			return err
		}
		switch {
		case exp <= now:
			oerr = errGrant("unknown or expired code")
		case cid != clientID:
			oerr = errGrant("code was issued to another client")
		case redirect != f.Get("redirect_uri"):
			oerr = errGrant("redirect_uri does not match the authorization request")
		case !validVerifierLike(verifier) || !constantEqual(pkceS256(verifier), challenge):
			oerr = errGrant("code_verifier does not match the code_challenge")
		case f.Get("resource") != "" && resource != "" && f.Get("resource") != resource:
			oerr = &oauthErr{Code: "invalid_target", Desc: "resource does not match the authorization request"}
		}
		if oerr != nil {
			return nil
		}
		g.keyID = keyID.String
		if err := json.Unmarshal([]byte(groups), &g.groups); err != nil {
			return fmt.Errorf("code groups: %w", err)
		}
		err = tx.QueryRow("SELECT name FROM oauth_clients WHERE id = ?", cid).Scan(&g.clientName)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return err
	})
	if err != nil {
		return serverErr(err)
	}
	if oerr != nil {
		return nil, oerr
	}

	created := g.keyID == ""
	if created {
		key, err := s.opts.Keys.CreateOAuth(g.keyName, clientID)
		if err != nil {
			return serverErr(fmt.Errorf("create key: %w", err))
		}
		g.keyID = key.ID
		if err := s.opts.Groups.SetKeyGroups(r.Context(), key.ID, g.groups); err != nil {
			s.dropKey(key.ID)
			return serverErr(fmt.Errorf("set groups of %s: %w", key.ID, err))
		}
	}
	family, err := randomToken()
	if err != nil {
		return serverErr(err)
	}
	var resp *tokenResponse
	err = s.opts.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE oauth_codes SET family = ?, key_id = ? WHERE hash = ? AND family IS NULL", family, g.keyID, hash)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			// A replay came in meanwhile, or a reused key was revoked (its
			// codes went with it).
			oerr = errGrant("code already used")
			return err
		}
		resp, err = s.issue(tx, g.keyID, family)
		return err
	})
	if (err != nil || oerr != nil) && created {
		s.dropKey(g.keyID)
	}
	if err != nil {
		return serverErr(err)
	}
	if oerr != nil {
		return nil, oerr
	}
	s.alog.Record(r.Context(), adminlog.Entry{Actor: g.approvedBy, Action: adminlog.OAuthAuthorize, Target: g.keyID, Detail: g.clientName, IP: g.approvedIP})
	return resp, nil
}

// dropKey removes a key made for an exchange that then failed.
func (s *Server) dropKey(id string) {
	if _, err := s.opts.Keys.Revoke(id); err != nil {
		s.log.Error("removing the key failed", "key", id, "error", err)
	}
}

func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) refresh(r *http.Request, clientID string) (*tokenResponse, *oauthErr) {
	token := r.PostForm.Get("refresh_token")
	if token == "" {
		return nil, &oauthErr{Code: "invalid_request", Desc: "refresh_token is required"}
	}
	var (
		resp *tokenResponse
		oerr *oauthErr
	)
	err := s.opts.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var keyID, family string
		var exp int64
		var consumed sql.NullInt64
		hash := auth.Hash(token)
		err := tx.QueryRow("SELECT key_id, family, expires_at, consumed_at FROM oauth_tokens WHERE hash = ? AND kind = 'refresh'", hash).
			Scan(&keyID, &family, &exp, &consumed)
		if errors.Is(err, sql.ErrNoRows) {
			oerr = errGrant("unknown or expired refresh token")
			return nil
		}
		if err != nil {
			return err
		}
		if consumed.Valid {
			// Rotation means a refresh token is used once. A second use is
			// either the attacker or the client after the attacker, and the
			// hub can't tell which: end the whole grant (RFC 9700 4.14.2).
			s.log.Warn("refresh token reused; revoking its grant", "key", keyID)
			if _, err := tx.Exec("DELETE FROM oauth_tokens WHERE family = ?", family); err != nil {
				return err
			}
			oerr = errGrant("refresh token already used; the authorization was revoked, sign in again")
			return nil
		}
		// Rotation: the old token dies with its first use, expired or not. It
		// stays as a tombstone until it would have expired.
		if _, err := tx.Exec("UPDATE oauth_tokens SET consumed_at = ? WHERE hash = ?", store.Millis(s.now()), hash); err != nil {
			return err
		}
		if exp <= store.Millis(s.now()) {
			oerr = errGrant("unknown or expired refresh token")
			return nil
		}
		var owner sql.NullString
		if err := tx.QueryRow("SELECT oauth_client FROM keys WHERE id = ?", keyID).Scan(&owner); err != nil {
			return err
		}
		if owner.String != clientID {
			oerr = errGrant("refresh token was issued to another client")
			return nil
		}
		resp, err = s.issue(tx, keyID, family)
		return err
	})
	if err != nil {
		s.log.Error("refreshing token failed", "error", err)
		return nil, &oauthErr{Code: "server_error", Desc: "could not issue tokens", Status: http.StatusInternalServerError}
	}
	return resp, oerr
}

// handleRevoke implements RFC 7009. Revoking a refresh token takes its
// grant's access tokens with it (its family); other grants of the same key
// stay.
// Connections are not closed: they belong to the client key, which may hold
// other valid tokens, and every request checks its token again, so only
// what is already running (a call, a session-based client's notification
// stream, a per-client process until it idles) outlives the token.
// Revoking the key closes all of that.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err == nil {
		if token := r.PostForm.Get("token"); token != "" {
			hash := auth.Hash(token)
			err := s.opts.DB.Tx(r.Context(), func(tx *sql.Tx) error {
				var family, kind string
				err := tx.QueryRow("SELECT family, kind FROM oauth_tokens WHERE hash = ?", hash).Scan(&family, &kind)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				if kind == "refresh" {
					_, err = tx.Exec("DELETE FROM oauth_tokens WHERE family = ?", family)
				} else {
					_, err = tx.Exec("DELETE FROM oauth_tokens WHERE hash = ?", hash)
				}
				return err
			})
			if err != nil {
				s.log.Error("revoking token failed", "error", err)
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
