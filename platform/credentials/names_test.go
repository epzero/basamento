package credentials

import "testing"

func TestValidName(t *testing.T) {
	valid := []string{
		"a",
		"Vendor.A_1/x-y",
		".a/b..c",
		"vendor/anthropic",
		"a.b",
		"a-b_c",
		"123",
		"a/..b",
	}
	for _, name := range valid {
		if !validName(name) {
			t.Errorf("validName(%q) = false, want true", name)
		}
	}
	invalid := []string{
		"",
		"vendor anthropic",
		"vendor/anthropic%",
		"vendor:anthropic",
		"a/./b/",
		"../a",
		"a/../b",
		"/a",
		"a/",
		"a//b",
		".",
		"..",
		"a\x00b",
		"a\nb",
		"é",
	}
	for _, name := range invalid {
		if validName(name) {
			t.Errorf("validName(%q) = true, want false", name)
		}
	}
	if validName(string(make([]byte, 128))) {
		t.Error("128 NUL bytes accepted")
	}
	long := make([]byte, 128)
	for i := range long {
		long[i] = 'a'
	}
	if !validName(string(long)) {
		t.Error("128 a's rejected")
	}
	if validName(string(append(long, 'a'))) {
		t.Error("129 characters accepted")
	}
}

func TestReferenceName(t *testing.T) {
	valid := map[string]string{
		"cred://a":                "a",
		"cred://Vendor.A_1/x-y":   "Vendor.A_1/x-y",
		"cred://vendor/anthropic": "vendor/anthropic",
	}
	for ref, want := range valid {
		got, ok := referenceName(ref)
		if !ok || got != want {
			t.Errorf("referenceName(%q) = (%q, %v), want (%q, true)", ref, got, ok, want)
		}
	}
	invalid := []string{
		"",
		"cred://",
		"vendor/anthropic",
		"vault://vendor/anthropic",
		"Cred://vendor/anthropic",
		"cred://vendor/anthropic%",
		"cred://vendor/anthropic?version=1",
		"cred://Vendor anthropic",
		"cred://.",
		"cred://a/./b",
		"cred://../a",
		"cred://a/../b",
		"cred:///a",
		"cred://a/",
		"cred://a//b",
	}
	for _, ref := range invalid {
		if name, ok := referenceName(ref); ok {
			t.Errorf("referenceName(%q) = (%q, true), want false", ref, name)
		}
	}
}
