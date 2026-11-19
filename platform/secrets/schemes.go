package secrets

// cloudKeeperSchemes returns offline-registered cloud KMS schemes.
func cloudKeeperSchemes() []string {
	return []string{"awskms", "gcpkms"}
}

func schemeSupported(scheme string) bool {
	switch scheme {
	case "localsecrets":
		return true
	default:
		for _, s := range cloudKeeperSchemes() {
			if s == scheme {
				return true
			}
		}
		return false
	}
}
