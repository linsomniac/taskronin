package domain

import (
	"database/sql"
	"strings"
	"time"
)

// ActorKind identifies the authority behind an operation.
type ActorKind uint8

const (
	// ActorOwner denotes the human owner.
	ActorOwner ActorKind = iota + 1
	// ActorToken denotes a named non-owner token.
	ActorToken ActorKind = iota + 1
	// ActorSystem denotes an internal operation.
	ActorSystem ActorKind = iota + 1
)

// Channel identifies the calling interface. It is informational, except that
// token management refuses the trusted, in-process web channel.
type Channel string

const (
	// ChanCLI denotes the command-line interface.
	ChanCLI Channel = "cli"
	// ChanTUI denotes the terminal interface.
	ChanTUI Channel = "tui"
	// ChanWeb denotes the in-process web interface.
	ChanWeb Channel = "web"
	// ChanMCP denotes the MCP interface.
	ChanMCP Channel = "mcp"
	// ChanAPI denotes the HTTP API.
	ChanAPI Channel = "api"
	// ChanSystem denotes an internal operation.
	ChanSystem Channel = "system"
)

// Valid reports whether c is a declared channel.
func (c Channel) Valid() bool {
	switch c {
	case ChanCLI, ChanTUI, ChanWeb, ChanMCP, ChanAPI, ChanSystem:
		return true
	default:
		return false
	}
}

// Scopes is a bit set of permissions.
type Scopes uint8

const (
	// ScopeRead permits reading.
	ScopeRead Scopes = 1 << iota
	// ScopeCapture permits capturing tasks and entries.
	ScopeCapture Scopes = 1 << iota
	// ScopeWrite permits ordinary mutations.
	ScopeWrite Scopes = 1 << iota
	// ScopeDestroy permits destructive mutations.
	ScopeDestroy Scopes = 1 << iota
	// ScopeAdmin permits administration.
	ScopeAdmin Scopes = 1 << iota
	// AllScopes contains every declared permission.
	AllScopes Scopes = ScopeRead | ScopeCapture | ScopeWrite | ScopeDestroy | ScopeAdmin
	// MCPDefaultScopes contains the default permissions for MCP tokens.
	MCPDefaultScopes Scopes = ScopeRead | ScopeCapture | ScopeWrite
)

var scopeNames = [...]struct {
	name string
	bit  Scopes
}{
	{"read", ScopeRead},
	{"capture", ScopeCapture},
	{"write", ScopeWrite},
	{"destroy", ScopeDestroy},
	{"admin", ScopeAdmin},
}

// ParseScopes parses comma-separated permission names; an empty string means
// no permissions, as used by system operations.
func ParseScopes(s string) (Scopes, error) {
	if s == "" {
		return 0, nil
	}
	var scopes Scopes
	for name := range strings.SplitSeq(s, ",") {
		var bit Scopes
		for _, scope := range scopeNames {
			if name == scope.name {
				bit = scope.bit
				break
			}
		}
		if bit == 0 {
			return 0, ValidationError(FieldError{Field: "scopes", Message: "unknown scope name"})
		}
		scopes |= bit
	}
	return scopes, nil
}

// Has reports whether s contains every permission in req.
func (s Scopes) Has(req Scopes) bool { return s&req == req }

// String returns names in canonical read,capture,write,destroy,admin order.
func (s Scopes) String() string {
	var names []string
	for _, scope := range scopeNames {
		if s.Has(scope.bit) {
			names = append(names, scope.name)
		}
	}
	return strings.Join(names, ",")
}

// Actor describes the authenticated caller of an operation.
type Actor struct {
	Kind             ActorKind
	TokenID          TokenID // zero for system and web-session owner
	TokenName        string  // set only for ActorToken
	Scopes           Scopes
	Channel          Channel
	MutationsPerHour int    // zero means unlimited
	RemoteKey        string // "socket" or client IP; password-gate bucket key
}

// Key returns the persisted actor key: owner, token:<name>, or system.
func (a Actor) Key() string {
	switch a.Kind {
	case ActorOwner:
		return "owner"
	case ActorToken:
		return "token:" + a.TokenName
	case ActorSystem:
		return "system"
	default:
		return ""
	}
}

// IsAI reports whether the actor is a non-owner token.
func (a Actor) IsAI() bool { return a.Kind == ActorToken }

// SystemActor returns the internal actor with channel system and no scopes.
func SystemActor() Actor { return Actor{Kind: ActorSystem, Channel: ChanSystem} }

// Token stores authentication token metadata and its secret hash.
type Token struct {
	ID               TokenID
	Name             string
	SecretHash       [32]byte
	Scopes           Scopes
	IsOwner          bool
	MutationsPerHour int
	CreatedAt        time.Time
	ExpiresAt        sql.Null[time.Time]
	LastUsedAt       sql.Null[time.Time]
	RevokedAt        sql.Null[time.Time]
}
