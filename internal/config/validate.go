package config

// Validate checks every setting rule (including URL syntax, REQ-CONFIG-017)
// and returns every error, one per offending key, in key order. Each error is
// a *core.Error with code invalid_argument that names the key and never echoes
// the value, except for a URL scheme that parsed (REQ-CONFIG-006, -007).
func (r Result) Validate() []error {
	var errs []error
	for _, s := range settings {
		if err := s.validate(r.Config.Profile, r.values[s.key]); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
