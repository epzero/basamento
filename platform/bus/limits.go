package bus

// casExpectHeaderMaxBytes is the largest JetStream CAS header
// (Nats-Expected-Last-Subject-Sequence) counting toward max_payload: NATS/1.0\r\n
// 10, name 35, ": " 2, revision up to 20 digits, \r\n 2, final \r\n 2.
const casExpectHeaderMaxBytes = 71

const minClusterMaxPayload = casExpectHeaderMaxBytes + deadlineSize + 1

// Limits are effective publish and KV value caps after server max_payload.
type Limits struct {
	MaxPublish int
	MaxKVValue int
}

// LimitsFor derives caps from configured MaxMessageSize and the connected server's max_payload.
func LimitsFor(configuredMaxMessage, serverMaxPayload int) Limits {
	pub := effectiveMaxMessageSize(configuredMaxMessage, serverMaxPayload)
	return Limits{MaxPublish: pub, MaxKVValue: effectiveMaxKVValue(pub)}
}

func effectiveMaxMessageSize(configured, serverMaxPayload int) int {
	if configured <= 0 {
		configured = DefaultMaxMessageSize
	}
	if serverMaxPayload <= 0 {
		return configured
	}
	room := serverMaxPayload - casExpectHeaderMaxBytes
	if room < 0 {
		return 0
	}
	if configured <= room {
		return configured
	}
	return room
}

func effectiveMaxKVValue(effectivePublish int) int {
	if effectivePublish <= deadlineSize {
		return 0
	}
	return effectivePublish - deadlineSize
}

func clusterMaxPayloadTooSmall(serverMaxPayload int) bool {
	return serverMaxPayload > 0 && serverMaxPayload < minClusterMaxPayload
}

// EmbeddedServerMaxPayload is the nats-server MaxPayload for OpenEmbedded.
func EmbeddedServerMaxPayload(configuredMaxMessage int) int32 {
	if configuredMaxMessage <= 0 {
		configuredMaxMessage = DefaultMaxMessageSize
	}
	return int32(configuredMaxMessage + casExpectHeaderMaxBytes)
}
