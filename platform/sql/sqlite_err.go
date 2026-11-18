package sql

import (
	"strings"
)

func sqliteConstraintCode(err error) int {
	if err == nil {
		return 0
	}
	s := strings.ToUpper(err.Error())
	if strings.Contains(s, "UNIQUE CONSTRAINT") ||
		strings.Contains(s, "FOREIGN KEY CONSTRAINT") ||
		strings.Contains(s, "CONSTRAINT FAILED") {
		return 1
	}
	return 0
}
