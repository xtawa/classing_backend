package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
)

func TestTokenRoundTripAndTamperDetection(t *testing.T) {
	manager := NewManager([]byte("01234567890123456789012345678901"), time.Minute)
	user := model.User{ID: "usr_test", Role: model.RoleAdmin, AuthEpoch: 42}
	token, _, err := manager.Issue(user)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != user.ID || claims.Role != user.Role || claims.Epoch != user.AuthEpoch {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	parts := strings.Split(token, ".")
	parts[1] = parts[1] + "x"
	if _, err := manager.Parse(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("StrongPass123!")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "StrongPass123!") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("invalid password accepted")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("weak password accepted")
	}
}
