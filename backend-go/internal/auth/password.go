package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory  = 64 * 1024
	passwordRounds  = 3
	passwordThreads = 2
	passwordKeyLen  = 32
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passwordRounds, passwordMemory, passwordThreads, passwordKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", passwordMemory, passwordRounds, passwordThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false
	}
	values := map[string]uint32{}
	for _, item := range params {
		pair := strings.SplitN(item, "=", 2)
		if len(pair) != 2 {
			return false
		}
		parsed, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil {
			return false
		}
		values[pair[0]] = uint32(parsed)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 || values["m"] < 8*1024 || values["m"] > 1024*1024 || values["t"] == 0 || values["t"] > 10 || values["p"] == 0 || values["p"] > 16 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, values["t"], values["m"], uint8(values["p"]), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
