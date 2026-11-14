package adminauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aihot/backend-go/internal/auth"
)

type AdminAccount struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	VerifiedAt   *time.Time `json:"verifiedAt"`
	IsAdmin      bool       `json:"isAdmin"`
}

type accountClaims struct {
	AccountID int64 `json:"accountId"`
}

type accountRepository interface {
	FindAccount(context.Context, int64, string) (AdminAccount, error)
	CreateAccountSession(context.Context, AdminAccount, string, string, string, time.Time, *string) error
	ListAccounts(context.Context, int64, int) ([]AdminAccount, error)
	SetAccountAdmin(context.Context, int64, int64, bool) error
}

/** AccountLogin authenticates a granted account into a separate admin session. */
func (s *Service) AccountLogin(ctx context.Context, email, password, returnTo, userAgent string) (string, string, error) {
	repo, ok := s.repo.(accountRepository)
	if !ok {
		return "", "", ErrLoginRejected
	}
	account, err := repo.FindAccount(ctx, 0, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, ErrNotFound) {
		return "", "", ErrLoginRejected
	}
	if err != nil {
		return "", "", err
	}
	if !auth.VerifyPassword(account.PasswordHash, password) || !account.IsAdmin || account.VerifiedAt == nil {
		return "", "", ErrLoginRejected
	}
	key, err := s.secret()
	if err != nil {
		return "", "", err
	}
	token, csrf, err := s.newSessionValues()
	if err != nil {
		return "", "", err
	}
	binding := accountBinding(account, key)
	if err := repo.CreateAccountSession(ctx, account, hashToken(token), csrf, binding, s.now().Add(SessionDays*24*time.Hour), truncatePointer(userAgent, 300)); err != nil {
		return "", "", err
	}
	target := SafeReturn(returnTo)
	if strings.HasPrefix(target, "/super") {
		target = "/admin"
	}
	return token, target, nil
}

/** authorizeSession rechecks account grants for every administrative request. */
func (s *Service) authorizeSession(ctx context.Context, record SessionRecord) (bool, bool, error) {
	if record.AuthMethod == "password" && (len(record.AuthClaims) == 0 || string(record.AuthClaims) == "null") {
		valid := s.sessionAuthorized(record)
		return valid, valid, nil
	}
	repo, ok := s.repo.(accountRepository)
	if !ok {
		return false, false, nil
	}
	var account AdminAccount
	var err error
	if record.AuthMethod == "password" {
		var claims accountClaims
		if json.Unmarshal(record.AuthClaims, &claims) != nil || claims.AccountID <= 0 {
			return false, false, nil
		}
		account, err = repo.FindAccount(ctx, claims.AccountID, "")
	} else if record.AuthMethod == "feishu" && s.sessionAuthorized(record) {
		claims, valid := decodeClaims(record.AuthClaims)
		if !valid || claims.Email == nil {
			return false, false, nil
		}
		account, err = repo.FindAccount(ctx, 0, *claims.Email)
	} else {
		return false, false, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !account.IsAdmin || account.VerifiedAt == nil {
		return false, false, nil
	}
	if record.AuthMethod == "password" {
		key, err := s.secret()
		if err != nil {
			return false, false, err
		}
		expected := accountBinding(account, key)
		return subtle.ConstantTimeCompare([]byte(expected), []byte(record.AuthBinding)) == 1, false, nil
	}
	return true, false, nil
}

/** accountBinding invalidates a session when its account password changes. */
func accountBinding(account AdminAccount, key []byte) string {
	return sessionBinding("account", fmt.Sprintf("%d:%s", account.ID, account.PasswordHash), key)
}

/** ListAccounts returns one page of accounts for super administrators. */
func (s *Service) ListAccounts(ctx context.Context, after int64) ([]AdminAccount, error) {
	repo, ok := s.repo.(accountRepository)
	if !ok {
		return nil, errors.New("account repository unavailable")
	}
	return repo.ListAccounts(ctx, after, 50)
}

/** SetAccountAdmin updates a user's administrator grant. */
func (s *Service) SetAccountAdmin(ctx context.Context, actor, accountID int64, granted bool) error {
	repo, ok := s.repo.(accountRepository)
	if !ok {
		return errors.New("account repository unavailable")
	}
	return repo.SetAccountAdmin(ctx, actor, accountID, granted)
}
