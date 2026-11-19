package secrets

// Payload is the shape of a Temporal run history payload before or after codec
// encryption.
type Payload struct {
	Metadata map[string][]byte
	Data     []byte
}

// EncodingEncrypted marks a payload ciphertext produced by this codec.
const EncodingEncrypted = "binary/encrypted"

// MetaEncryptionKeyID names the Tink data key id in encrypted payload metadata.
const MetaEncryptionKeyID = "encryption-key-id"
