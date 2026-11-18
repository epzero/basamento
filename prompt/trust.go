package prompt

// CanonicalTrust normalises an input trust for storage, hashing and comparison.
// Empty means untrusted, matching LintTemplate and inputIndex.
func CanonicalTrust(t Trust) Trust {
	if t == TrustTrusted {
		return TrustTrusted
	}
	return TrustUntrusted
}
