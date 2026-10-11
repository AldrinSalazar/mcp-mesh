package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// maxPasswordBytes bounds the work a sign-in request can make us do.
const maxPasswordBytes = 1024

// Parameters for new hashes. OWASP's argon2id minimum is m=19 MiB, t=2, p=1;
// t=3 with 32 MiB sits above it and costs roughly 50-100 ms on a small box.
// The hub may be a Raspberry Pi or a 512 MB VM, so 64 MiB per concurrent
// sign-in would be too much; Users also caps concurrent hashes. Verify reads
// the parameters from the stored hash, so these can be raised later.
const (
	argonTime    = 3
	argonMemory  = 32 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

type argonParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
}

var currentParams = argonParams{argonMemory, argonTime, argonThreads}

// hashPassword returns a PHC string: $argon2id$v=19$m=..,t=..,p=..$salt$hash.
func hashPassword(password string) (string, error) {
	return hashWith(password, currentParams)
}

func hashWith(password string, p argonParams) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("random salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed password hash")

// parseHash splits a PHC string into its parameters, salt and key.
func parseHash(encoded string) (p argonParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errBadHash
	}
	if v, ok := strings.CutPrefix(parts[2], "v="); !ok || v != strconv.Itoa(argon2.Version) {
		return p, nil, nil, fmt.Errorf("%w: unsupported version %q", errBadHash, parts[2])
	}
	var m, t, th uint64
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return p, nil, nil, errBadHash
		}
		n, perr := strconv.ParseUint(v, 10, 32)
		if perr != nil {
			return p, nil, nil, errBadHash
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			th = n
		default:
			return p, nil, nil, errBadHash
		}
	}
	// Refuse absurd values so a corrupted row can't make us allocate GiBs.
	if m == 0 || t == 0 || th == 0 || m > 1<<20 || t > 100 || th > 255 {
		return p, nil, nil, fmt.Errorf("%w: parameters out of range", errBadHash)
	}
	enc := base64.RawStdEncoding
	if salt, err = enc.DecodeString(parts[4]); err != nil || len(salt) == 0 {
		return p, nil, nil, errBadHash
	}
	if key, err = enc.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return p, nil, nil, errBadHash
	}
	return argonParams{uint32(m), uint32(t), uint8(th)}, salt, key, nil
}

// verifyPassword reports whether password matches the PHC string.
func verifyPassword(password, encoded string) (bool, error) {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// checkPassword enforces the password policy.
func checkPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return ErrWeakPassword
	}
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("password is longer than %d bytes: %w", maxPasswordBytes, ErrWeakPassword)
	}
	return nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// spendDummy burns the time of one verification, so an unknown username
// takes as long to reject as a wrong password.
func spendDummy(password string) {
	dummyOnce.Do(func() { dummyHash, _ = hashPassword("dummy password for timing") })
	_, _ = verifyPassword(password, dummyHash)
}
