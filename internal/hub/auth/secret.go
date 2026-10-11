package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// secretDigits is how many base62 digits 32 random bytes need (62^43 > 2^256).
const secretDigits = 43

// NewSecret returns a fresh secret such as "mm_node_<43 base62 chars>". The
// prefix names the kind and lets secret scanners recognize a leaked key.
func NewSecret(kind Kind) (string, error) {
	if !validKind(kind) {
		return "", fmt.Errorf("unknown key kind %q", kind)
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random key: %w", err)
	}
	digits := new(big.Int).SetBytes(b[:]).Text(62)
	return prefix(kind) + strings.Repeat("0", secretDigits-len(digits)) + digits, nil
}

// Hash returns the hex SHA-256 of secret, which is what the store keeps.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// KindOf reads the kind from a secret's prefix. It doesn't prove the secret is
// valid, only that it is shaped like one.
func KindOf(secret string) (Kind, bool) {
	for _, kind := range kinds {
		if strings.HasPrefix(secret, prefix(kind)) && len(secret) == len(prefix(kind))+secretDigits {
			return kind, true
		}
	}
	return "", false
}

var kinds = []Kind{KindNode, KindClient}

func prefix(kind Kind) string { return "mm_" + string(kind) + "_" }

func validKind(kind Kind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}
