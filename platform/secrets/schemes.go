package secrets

// CloudKeeperSchemes lists master-key URL schemes implemented in this module
// (offline drivers registered via blank imports in schemes_register.go).
var CloudKeeperSchemes = []string{"awskms", "gcpkms"}

func schemeSupported(scheme string) bool {
	switch scheme {
	case "localsecrets":
		return true
	default:
		for _, s := range CloudKeeperSchemes {
			if s == scheme {
				return true
			}
		}
		return false
	}
}
