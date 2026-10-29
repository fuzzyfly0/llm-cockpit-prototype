package platform

import "testing"

func TestSessionURL(t *testing.T) {
	if got := SessionURL("https://firma.atlassian.net/browse/", "PROJ-123"); got != "https://firma.atlassian.net/browse/PROJ-123" {
		t.Errorf("append: %s", got)
	}
	if got := SessionURL("https://tracker/issues?q={name}&x=1", "login fix"); got != "https://tracker/issues?q=login%20fix&x=1" {
		t.Errorf("placeholder: %s", got)
	}
}

func TestWebURL(t *testing.T) {
	tests := map[string]string{
		"git@github.com:stefa/llm-cockpit.git":         "https://github.com/stefa/llm-cockpit",
		"https://github.com/stefa/llm-cockpit.git":     "https://github.com/stefa/llm-cockpit",
		"https://user:token@github.com/stefa/repo.git": "https://github.com/stefa/repo",
		"C:/repos/local": "",
	}
	for in, want := range tests {
		if got := WebURL(in); got != want {
			t.Errorf("WebURL(%q) = %q, want %q", in, got, want)
		}
	}
}
