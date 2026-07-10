package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	Subject string `json:"sub"`
	Role    string `json:"role"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
	ID      string `json:"jti"`
	Epoch   int64  `json:"aep"`
}

type Manager struct {
	secret []byte
	ttl    time.Duration
}

func NewManager(secret []byte, ttl time.Duration) *Manager {
	return &Manager{secret: secret, ttl: ttl}
}

func (m *Manager) Issue(user model.User) (string, int64, error) {
	now := time.Now()
	claims := Claims{
		Subject: user.ID,
		Role:    user.Role,
		Issued:  now.Unix(),
		Expires: now.Add(m.ttl).Unix(),
		ID:      ids.New("jti"),
		Epoch:   user.AuthEpoch,
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", 0, err
	}
	unsigned := encode(header) + "." + encode(payload)
	signature := m.sign(unsigned)
	return unsigned + "." + signature, claims.Expires * 1000, nil
}

func (m *Manager) Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("invalid token format")
	}
	expected := m.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return Claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errors.New("invalid token payload")
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, errors.New("invalid token claims")
	}
	if claims.Subject == "" || claims.Expires <= time.Now().Unix() {
		return Claims{}, errors.New("token expired")
	}
	if claims.Role != model.RoleAdmin && claims.Role != model.RoleUser {
		return Claims{}, errors.New("invalid token role")
	}
	return claims, nil
}

func (m *Manager) sign(value string) string {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func encode(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func HashPassword(password string) (string, error) {
	if len(password) < 8 || len(password) > 128 {
		return "", fmt.Errorf("password must contain 8 to 128 characters")
	}
	value, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(value), err
}

func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func HashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
