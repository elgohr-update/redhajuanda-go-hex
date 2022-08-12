package auth

import (
	"context"
	"go-hex/configs"
	"go-hex/internal/domain"
	"go-hex/internal/repository/port"
	"go-hex/pkg/auth"
	"go-hex/pkg/otel"
	"go-hex/pkg/password"
	"go-hex/pkg/times"
	"go-hex/shared/ierr"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
)

// Service encapsulates the authentication logic.
type Service struct {
	cfg         *configs.Config
	repoRegitry port.RepositoryRegistry
}

// NewService creates and returns a new auth service
func NewService(cfg *configs.Config, repoRegitry port.RepositoryRegistry) *Service {
	return &Service{cfg, repoRegitry}
}

// Login authenticates a user and generates a JWT token if authentication succeeds.
// Otherwise, an error is returned.
func (s *Service) Login(ctx context.Context, req RequestLogin) (ResponseLogin, error) {

	ctx, span := otel.Start(ctx)
	defer span.End()

	var res ResponseLogin

	err := req.Validate()
	if err != nil {
		return res, err
	}

	identity, err := s.authenticate(ctx, req.Username, req.Password)
	if err != nil {
		return res, err
	}

	accessToken, expiresAt, refreshToken, err := s.generateJWT(ctx, identity)
	return ResponseLogin{
		AccessToken:  accessToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		RefreshToken: refreshToken,
	}, err

}

// RefreshToken refresh the access token
func (s *Service) RefreshToken(ctx context.Context, req RequestRefreshToken) (ResponseLogin, error) {

	ctx, span := otel.Start(ctx)
	defer span.End()

	var res ResponseLogin

	err := req.Validate()
	if err != nil {
		return res, err
	}

	token, err := auth.VerifyToken(req.RefreshToken, s.cfg.JWT.SigningKey)
	if err != nil {
		return res, ierr.ErrInvalidToken
	}
	claims := token.Claims.(jwt.MapClaims)
	var tokenType string
	if val, ok := claims["token_type"].(string); ok {
		tokenType = val
	}

	if tokenType != TokenTypeRefresh {
		return res, ierr.ErrInvalidToken
	}

	var id string
	if val, ok := claims["id"].(string); ok {
		id = val
	}

	repoUser := s.repoRegitry.GetUserRepository()
	user, err := repoUser.GetByID(ctx, id)
	if err != nil {
		return res, err
	}

	if !password.ComparePasswords(*user.RefreshToken, []byte(req.RefreshToken)) {
		return res, ierr.ErrExpiredToken
	}

	accessToken, expiresAt, refreshToken, err := s.generateJWT(ctx, user)
	return ResponseLogin{
		AccessToken:  accessToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		RefreshToken: refreshToken,
	}, err
}

// authenticate authenticates a user using username and password.
// if username and password are correct, an identity is returned. Otherwise, nil is returned.
func (s *Service) authenticate(ctx context.Context, username, plainPwd string) (Identity, error) {

	ctx, span := otel.Start(ctx)
	defer span.End()

	repoUser := s.repoRegitry.GetUserRepository()
	user, err := repoUser.GetByUsername(ctx, username)
	if err != nil {
		if err == ierr.ErrResourceNotFound {
			return nil, ierr.ErrInvalidCreds
		}
		return nil, err
	}

	if username == user.GetUsername() && password.ComparePasswords(user.GetPassword(), []byte(plainPwd)) {
		// user is not active
		if !user.IsActive {
			return nil, ierr.ErrUserIsNotActive
		}
		// authentication successful
		return user, nil
	}

	// authentication failed
	return nil, ierr.ErrInvalidCreds

}

// generateJWT generates a JWT
func (s *Service) generateJWT(ctx context.Context, identity Identity) (accessToken string, expiresAt time.Time, refreshToken string, err error) {

	ctx, span := otel.Start(ctx)
	defer span.End()

	//generate access token
	accessToken, expiresAt, err = s.generateAccessToken(ctx, identity)
	if err != nil {
		return
	}
	// generate refresh token
	refreshToken, err = s.generateRefreshToken(ctx, identity)
	if err != nil {
		return
	}

	// hash refresh token
	hashedRefreshToken, err := password.HashAndSalt([]byte(refreshToken))
	user := domain.User{
		ID:           identity.GetID(),
		RefreshToken: &refreshToken,
	}
	if err != nil {
		return
	}
	user.RefreshToken = &hashedRefreshToken
	repoUser := s.repoRegitry.GetUserRepository()
	err = repoUser.Update(ctx, identity.GetID(), user)
	return
}

func (s *Service) generateAccessToken(ctx context.Context, identity Identity) (accessToken string, expiresAt time.Time, err error) {

	_, span := otel.Start(ctx)
	defer span.End()

	expiresAt = times.Now().Add(time.Duration(s.cfg.JWT.TokenExpiration) * time.Minute)
	expiresAtUnix := times.Now().Add(time.Duration(s.cfg.JWT.TokenExpiration) * time.Minute).Unix()
	accessToken, err = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":         identity.GetID(),
		"username":   identity.GetUsername(),
		"exp":        expiresAtUnix,
		"token_type": TokenTypeAccess,
	}).SignedString([]byte(s.cfg.JWT.SigningKey))
	err = errors.Wrap(err, "cannot generate token")
	return
}

func (s *Service) generateRefreshToken(ctx context.Context, identity Identity) (refreshToken string, err error) {

	_, span := otel.Start(ctx)
	defer span.End()

	refreshToken, err = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":         identity.GetID(),
		"exp":        times.Now().AddDate(1000, 0, 0).Unix(),
		"token_type": TokenTypeRefresh,
	}).SignedString([]byte(s.cfg.JWT.SigningKey))
	err = errors.Wrap(err, "cannot generate token")
	return
}
