package auth

import "testing"

func TestNewTokenStoresOnlyHash(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || hash == "" || hash == token {
		t.Fatal("expected opaque token and separate hash")
	}
	if HashToken(token) != hash {
		t.Fatal("token hash is not deterministic")
	}
	other, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if other == token {
		t.Fatal("token generator repeated a token")
	}
}
