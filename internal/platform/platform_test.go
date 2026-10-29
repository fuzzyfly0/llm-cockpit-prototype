package platform

import "testing"

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
