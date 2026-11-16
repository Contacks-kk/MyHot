package account

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"time"

	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/mailer"
)

var (
	ErrRegistrationClosed = errors.New("registration is closed")
	ErrNoSession          = errors.New("session does not exist")
	ErrDuplicateAccount   = errors.New("account already exists")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnverifiedAccount  = errors.New("account is not verified")
	ErrInvalidToken       = errors.New("email token is invalid or expired")
	ErrPasswordMismatch   = errors.New("password does not match")
	ErrVerificationMail   = errors.New("verification email could not be sent")
)

type Service struct {
	repository   *Repository
	config       config.Config
	mail         mailer.Sender
	verification *verificationCache
}

func NewService(repository *Repository, cfg config.Config, sender mailer.Sender) *Service {
	return &Service{repository: repository, config: cfg, mail: sender, verification: newVerificationCache()}
}

func (s *Service) ResolveSession(ctx context.Context, rawToken string) (Session, error) {
	value, err := s.repository.FindSession(ctx, auth.HashToken(rawToken))
	if errors.Is(err, ErrNotFound) {
		return Session{}, ErrNoSession
	}
	return value, err
}

func (s *Service) Register(ctx context.Context, email, password, code string) (LoginResult, error) {
	var result LoginResult
	err := s.verification.consume(email, code, func() error {
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		token, tokenHash, err := auth.NewToken()
		if err != nil {
			return err
		}
		csrf, err := randomText(32)
		if err != nil {
			return err
		}
		id, err := s.repository.Register(ctx, email, hash, tokenHash, csrf, time.Now().Add(30*24*time.Hour))
		if err != nil {
			return err
		}
		result = LoginResult{UserID: id, Email: email, Token: token, CSRF: csrf}
		return nil
	})
	return result, err
}

type LoginResult struct {
	UserID int64
	Email  string
	Token  string
	CSRF   string
}

func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	account, err := s.repository.LoginRecord(ctx, email)
	if errors.Is(err, ErrNotFound) || (err == nil && !auth.VerifyPassword(account.PasswordHash, password)) {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	if !account.VerifiedAt.Valid {
		return LoginResult{}, ErrUnverifiedAccount
	}
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrf, err := randomText(32)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.repository.CreateSession(ctx, account.ID, tokenHash, csrf, time.Now().Add(30*24*time.Hour)); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{UserID: account.ID, Email: email, Token: token, CSRF: csrf}, nil
}

func (s *Service) Logout(ctx context.Context, userID int64, rawToken string) {
	_ = s.repository.DeleteSession(ctx, userID, auth.HashToken(rawToken))
}

func (s *Service) DeleteAccount(ctx context.Context, userID int64, password string) error {
	hash, err := s.repository.PasswordHash(ctx, userID)
	if err != nil || !auth.VerifyPassword(hash, password) {
		return ErrPasswordMismatch
	}
	return s.repository.DeleteAccount(ctx, userID, s.config.SourceSchedule)
}

func (s *Service) VerifyEmail(ctx context.Context, email, code string) error {
	return s.verification.consume(email, code, func() error {
		err := s.repository.VerifyEmail(ctx, email)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidToken
		}
		return err
	})
}

func (s *Service) ResendVerification(ctx context.Context, email string) (VerificationTiming, error) {
	status, err := s.repository.AccountByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return VerificationTiming{}, err
	}
	if err == nil && status.VerifiedAt.Valid {
		return VerificationTiming{}, ErrDuplicateAccount
	}
	if errors.Is(err, ErrNotFound) && !s.config.OpenRegistration {
		return VerificationTiming{}, ErrRegistrationClosed
	}
	return s.verification.send(email, func(code string) error {
		if err := s.mail.Send(email, "MyHOT 邮箱验证码", "你的验证码是："+code+"\n\n验证码60秒内有效，仅可使用一次。如果这不是你的操作，请忽略此邮件。"); err != nil {
			return fmt.Errorf("%w: %v", ErrVerificationMail, err)
		}
		return nil
	})
}

func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	userID, err := s.repository.VerifiedAccountID(ctx, email)
	if err != nil {
		return nil
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		return nil
	}
	if err = s.repository.ReplaceEmailToken(ctx, userID, "password_reset", hash, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	link := s.config.PublicURL + "/password-reset?token=" + url.QueryEscape(token)
	return s.mail.Send(email, "MyHOT 密码重置", "请打开以下链接重置密码：\n\n"+link+"\n\n链接 1 小时内有效。")
}

func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	if len(token) < 32 || len(token) > 128 || len(password) < 12 || len(password) > 256 {
		return ErrInvalidToken
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	err = s.repository.ResetPassword(ctx, auth.HashToken(token), hash)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidToken
	}
	return err
}

func randomText(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
