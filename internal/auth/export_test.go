package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// hashForTest keeps gate tests fast without weakening production parameters.
func hashForTest(pw string) string {
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(pw), salt[:], 1, 8, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=8,t=1,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(key))
}
