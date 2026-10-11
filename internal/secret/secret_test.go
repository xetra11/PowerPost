package secret

import "testing"

func TestRoundTrip(t *testing.T) {
	b, err := Open(NewKey())
	if err != nil {
		t.Fatal(err)
	}
	sealed := b.Seal("token-123")
	if got, err := b.Unseal(sealed); err != nil || got != "token-123" {
		t.Fatalf("got %q, %v", got, err)
	}
	other, _ := Open(NewKey())
	if _, err := other.Unseal(sealed); err == nil {
		t.Fatal("wrong key must fail")
	}
	if b.Seal("") != nil {
		t.Fatal("empty stays empty")
	}
	if _, err := Open("short"); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestLoadOrCreatePersistsKey(t *testing.T) {
	dir := t.TempDir()
	b1, created, err := LoadOrCreate("", dir)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	b2, created, err := LoadOrCreate("", dir)
	if err != nil || created {
		t.Fatalf("second call created=%v err=%v", created, err)
	}
	if got, _ := b2.Unseal(b1.Seal("x")); got != "x" {
		t.Fatal("key not reused")
	}
}
