// Package auth holds the hub's credentials: bearer keys for nodes and clients,
// and admin users with passwords and sessions for the dashboard. Everything is
// stored in the hub database (shared by the hub and its CLI), keys as SHA-256
// hashes and passwords as argon2id hashes; the HTTP checks that use them live
// here too.
package auth

import "time"

// Kind says what a key may do.
type Kind string

const (
	// KindNode lets a node connect and register MCPs under the key's Name.
	KindNode Kind = "node"
	// KindClient lets an MCP client call tools. Its Name is shown in activity.
	KindClient Kind = "client"
)

// Key is a stored credential. The secret itself is never stored, only its
// SHA-256 hash: keys are 32 random bytes, so a slow hash isn't needed.
type Key struct {
	// ID identifies the key in lists and revocation ("k_" + 8 hex chars).
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Name is the node name a node key is bound to, or a label for client
	// keys (e.g. "laptop-claude").
	Name    string    `json:"name"`
	Hash    string    `json:"hash"` // hex SHA-256 of the full secret
	Created time.Time `json:"created"`
	// LastUsed and LastIP show in the dashboard; they are saved to the
	// database at most once a minute per key.
	LastUsed time.Time `json:"-"`
	LastIP   string    `json:"-"`
	// OAuthClient is set on the client key behind an OAuth authorization:
	// the ID of the OAuth client (oauth_clients.id) it was granted to.
	OAuthClient string `json:"-"`
}
