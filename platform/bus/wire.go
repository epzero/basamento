package bus

import "strings"

// busSubjectPrefix namespaces JetStream publishes so the bus stream does not
// capture JetStream KV control subjects ($KV.*).
const busSubjectPrefix = "kit.bus."

// wireSubject maps a logical bus subject to the NATS subject stored in JetStream.
func wireSubject(logical string) string {
	return busSubjectPrefix + logical
}

// logicalSubject maps a stored NATS subject back to the logical bus subject.
func logicalSubject(wire string) string {
	return strings.TrimPrefix(wire, busSubjectPrefix)
}
