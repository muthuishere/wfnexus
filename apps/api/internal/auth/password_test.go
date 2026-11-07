package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashIsArgon2idAndVerifies(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("not a PHC argon2id hash: %s", h)
	}
	if strings.Contains(h, "correct horse") {
		t.Fatal("the hash contains the password")
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Fatal("the right password did not verify")
	}
	if VerifyPassword(h, "correct horse batterY") {
		t.Fatal("a wrong password verified")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("two hashes of one password are equal: no salt")
	}
}

func TestEmptyOrMalformedHashNeverVerifies(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$$"} {
		if VerifyPassword(h, "") || VerifyPassword(h, "anything-at-all") {
			t.Fatalf("hash %q verified", h)
		}
	}
}

func TestPasswordMinimumLengthIsTwelve(t *testing.T) {
	if ValidatePassword("12345678901") == nil {
		t.Fatal("11 characters accepted")
	}
	if err := ValidatePassword("123456789012"); err != nil {
		t.Fatalf("12 characters refused: %v", err)
	}
	if ValidatePassword("ééééééééééé") == nil { // 11 runes, 22 bytes
		t.Fatal("length counted in bytes, not characters")
	}
}

func TestNewPasswordMeetsTheRule(t *testing.T) {
	a, b := NewPassword(), NewPassword()
	if ValidatePassword(a) != nil || a == b {
		t.Fatalf("generated %q %q", a, b)
	}
}
