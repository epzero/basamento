package sqltest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/epzero/basamento/core"
)

// ErrorContainsSecret reports whether err exposes secret in message or details.
func ErrorContainsSecret(err error, secret string) bool {
	if err == nil || secret == "" {
		return false
	}
	if strings.Contains(err.Error(), secret) {
		return true
	}
	var pe *core.Error
	if errors.As(err, &pe) && pe.Details != nil {
		for _, v := range pe.Details {
			if strings.Contains(fmt.Sprint(v), secret) {
				return true
			}
		}
	}
	return false
}
