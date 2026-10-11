package config

import "testing"

func TestNgrokDomainSetsPublicBaseURL(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("NGROK_DOMAIN", "https://name.ngrok-free.dev/")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.NgrokDomain != "name.ngrok-free.dev" || c.PublicBaseURL != "https://name.ngrok-free.dev" || !c.NgrokAutostart {
		t.Fatalf("got domain %q base %q autostart %v", c.NgrokDomain, c.PublicBaseURL, c.NgrokAutostart)
	}

	t.Setenv("PUBLIC_BASE_URL", "https://pp.example.com")
	t.Setenv("NGROK_AUTOSTART", "false")
	c, _ = Load()
	if c.PublicBaseURL != "https://pp.example.com" || c.NgrokAutostart {
		t.Fatalf("explicit PUBLIC_BASE_URL must win: %q autostart %v", c.PublicBaseURL, c.NgrokAutostart)
	}
}
