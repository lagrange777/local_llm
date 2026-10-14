package web

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

type passwordRecord struct {
	iterations   int
	salt, digest []byte
}

func parsePasswordHash(value string) (passwordRecord, error) {
	invalid := fmt.Errorf("invalid web password hash: expected PBKDF2-SHA256 with 600000-1000000 iterations")
	parts := strings.Split(value, "$")
	if len(value) > 512 || len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return passwordRecord{}, invalid
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 600000 || iterations > 1000000 {
		return passwordRecord{}, invalid
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return passwordRecord{}, invalid
	}
	digest, err := base64.RawStdEncoding.Strict().DecodeString(parts[3])
	if err != nil || len(digest) != 32 {
		return passwordRecord{}, invalid
	}
	return passwordRecord{iterations: iterations, salt: salt, digest: digest}, nil
}
func (p passwordRecord) verify(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	digest, err := pbkdf2.Key(sha256.New, value, p.salt, p.iterations, 32)
	return err == nil && subtle.ConstantTimeCompare(digest, p.digest) == 1
}
