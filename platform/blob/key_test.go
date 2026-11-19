package blob

import "testing"

func TestValidateContentKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key string
		ok  bool
	}{
		{"", false},
		{"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", true},
		{"sha256:2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824", false},
		{"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b982", false},
		{"md5:5d41402abc4b2a76b9719d911017c592", false},
		{"sha256:../../../etc/passwd", false},
	}
	for _, tc := range tests {
		if got := ValidateContentKey(tc.key); got != tc.ok {
			t.Errorf("ValidateContentKey(%q) = %v, want %v", tc.key, got, tc.ok)
		}
	}
}

func TestContentKeyOf(t *testing.T) {
	t.Parallel()
	const want = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := ContentKeyOf([]byte("hello")); got != want {
		t.Fatalf("ContentKeyOf(hello) = %q, want %q", got, want)
	}
}
