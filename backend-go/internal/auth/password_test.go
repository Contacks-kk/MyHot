package auth

import "testing"

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if VerifyPassword(hash, "correct horse battery staple") != true {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword(hash, "incorrect password") {
		t.Fatal("unexpected password verification")
	}
	if VerifyPassword("not-a-hash", "correct horse battery staple") {
		t.Fatal("malformed hash verified")
	}
}
