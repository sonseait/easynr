package config

import "testing"

func TestConfiguration(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("PUBLIC_URL", "")
	c, err := Parse(nil)
	if err != nil || c.Listen != "0.0.0.0:4873" || c.Data != "./data" {
		t.Fatal(c, err)
	}
	for _, v := range []string{"0", "65536", "-1", "+1", "a", " 4873"} {
		t.Setenv("PORT", v)
		if _, err := Parse(nil); err == nil {
			t.Fatal("accepted PORT", v)
		}
	}
	t.Setenv("PORT", "8080")
	t.Setenv("PUBLIC_URL", "https://npm.example.com/")
	c, err = Parse(nil)
	if err != nil || c.PublicURL != "https://npm.example.com" {
		t.Fatal(c, err)
	}
	for _, v := range []string{"ftp://x", "https://u:p@x", "https://x/path", "https://x?secret=a", "https://x#fragment"} {
		t.Setenv("PUBLIC_URL", v)
		if _, err := Parse(nil); err == nil {
			t.Fatal("accepted PUBLIC_URL", v)
		}
	}
}
