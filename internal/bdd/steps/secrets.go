package steps

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

type secretsWorld struct {
	sim                    *simKeyRegistry
	store                  secrets.KeysetStore
	codec                  *secrets.Codec
	codec2                 *secrets.Codec
	port                   *secrets.Port
	portURL                string
	payloads               map[string]secrets.Payload
	encoded                map[string]secrets.Payload
	decoded                map[string]secrets.Payload
	lastErr                error
	lastOp                 string
	logBuf                 *bytes.Buffer
	logHandler             slog.Handler
	ciphertext             []byte
	lastEnc                secrets.Payload
	lastDec                secrets.Payload
	valuePlain             []byte
	lastVal                []byte
	lastDecodedPayloadName string
	unwrapAtRestart        int
	lastCodecUnwraps       int
	storeSnaps             map[string]secrets.StoredKeyset
}

func newSecretsWorld() *secretsWorld {
	w := &secretsWorld{
		payloads: map[string]secrets.Payload{},
		encoded:  map[string]secrets.Payload{},
		decoded:  map[string]secrets.Payload{},
	}
	w.logBuf = &bytes.Buffer{}
	w.logHandler = slog.NewTextHandler(w.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return w
}

type simKeyRegistry struct {
	mu               sync.Mutex
	keys             map[string][]byte
	unreachable      bool
	failAfterRewraps int
	rewraps          int
	unwraps          int
	unwrapByCodec    map[*secrets.Codec]int
	plainByTenant    map[string][]byte
	nonceSeq         uint64
}

func newSimRegistry() *simKeyRegistry {
	return &simKeyRegistry{
		keys:          map[string][]byte{},
		unwrapByCodec: map[*secrets.Codec]int{},
	}
}

func (r *simKeyRegistry) hold(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.keys[name]; !ok {
		sum := sha256.Sum256([]byte("basamento.sim.mk:" + name))
		r.keys[name] = append([]byte(nil), sum[:]...)
	}
}

func (r *simKeyRegistry) delete(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, name)
}

type simKeyService struct {
	reg   *simKeyRegistry
	name  string
	codec *secrets.Codec
}

func (s *simKeyService) MasterKeyID() string { return s.name }

func (s *simKeyService) Close() error { return nil }

func (s *simKeyService) Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error) {
	_ = ctx
	if err := s.reg.checkAvailable(true, tenant); err != nil {
		return nil, err
	}
	s.reg.noteKeysetPlain(tenant, plaintext)
	return s.encrypt(tenant, plaintext)
}

func (s *simKeyService) Unwrap(ctx context.Context, tenant string, ciphertext []byte) ([]byte, error) {
	_ = ctx
	if err := s.reg.checkAvailable(false, tenant); err != nil {
		return nil, err
	}
	s.reg.countUnwrap(s.codec)
	return s.decrypt(tenant, ciphertext)
}

func (s *simKeyService) encrypt(tenant string, plain []byte) ([]byte, error) {
	inner, err := secrets.FrameBoundPlaintext(tenant, plain)
	if err != nil {
		return nil, err
	}
	key := s.reg.keyBytes(s.name)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := s.reg.nextNonce(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, inner, nil), nil
}

func (s *simKeyService) decrypt(tenant string, ct []byte) ([]byte, error) {
	key := s.reg.keyBytes(s.name)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < gcm.NonceSize() {
		return nil, core.NewError(core.CodeInvalidArgument, "value cannot be decrypted", nil)
	}
	nonce, body := ct[:gcm.NonceSize()], ct[gcm.NonceSize():]
	inner, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "value cannot be decrypted", nil)
	}
	out, err := secrets.ParseBoundPlaintext(tenant, inner)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *simKeyRegistry) keyBytes(name string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.keys[name]...)
}

func (r *simKeyRegistry) checkAvailable(wrap bool, tenant string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unreachable {
		return core.NewError(core.CodeUnavailable, "simulated", nil)
	}
	if wrap && tenant != "__basamento_canary__" && r.failAfterRewraps > 0 {
		r.rewraps++
		if r.rewraps > r.failAfterRewraps {
			return &core.Error{Code: core.CodeUnavailable, Message: "simulated", Retryable: true}
		}
	}
	return nil
}

func (r *simKeyRegistry) countUnwrap(codec *secrets.Codec) {
	r.mu.Lock()
	r.unwraps++
	if codec != nil {
		r.unwrapByCodec[codec]++
	}
	r.mu.Unlock()
}

func (r *simKeyRegistry) unwrapForCodec(codec *secrets.Codec) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unwrapByCodec[codec]
}

func (r *simKeyRegistry) nextNonce(size int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nonceSeq++
	nonce := make([]byte, size)
	if size >= 8 {
		binary.BigEndian.PutUint64(nonce[size-8:], r.nonceSeq)
	}
	return nonce
}

func (r *simKeyRegistry) noteKeysetPlain(tenant string, plain []byte) {
	r.mu.Lock()
	if r.plainByTenant == nil {
		r.plainByTenant = map[string][]byte{}
	}
	r.plainByTenant[tenant] = append([]byte(nil), plain...)
	r.mu.Unlock()
}

func (r *simKeyRegistry) keysetPlain(tenant string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.plainByTenant[tenant]...)
}

func (r *simKeyRegistry) unwrapTotal() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unwraps
}

func bytesFromSeed(seed int64, n int) []byte {
	var out []byte
	h := sha256.Sum256([]byte(fmt.Sprintf("basamento.seed.%d", seed)))
	for len(out) < n {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:n]
}

func localKeyURLFromSeed(seed int64) string {
	key := bytesFromSeed(seed, 32)
	return "localsecrets://" + base64.StdEncoding.EncodeToString(key)
}

func tenantCtx(ctx context.Context, tenant string) context.Context {
	return core.WithTenant(ctx, core.TenantID(tenant))
}

func registerSecrets(sc *godog.ScenarioContext) {
	sc.Step(`^the simulated key service holds master key "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx)
		if w.Secrets == nil {
			w.Secrets = newSecretsWorld()
		}
		if w.Secrets.sim == nil {
			w.Secrets.sim = newSimRegistry()
		}
		w.Secrets.sim.hold(name)
		return nil
	})
	sc.Step(`^the simulated key service also holds master key "([^"]*)"$`, func(ctx context.Context, name string) error {
		secretsW(ctx).sim.hold(name)
		return nil
	})
	sc.Step(`^the simulated key service is unreachable$`, func(ctx context.Context) error {
		w := world(ctx).Secrets
		w.sim.mu.Lock()
		w.sim.unreachable = true
		w.sim.mu.Unlock()
		return nil
	})
	sc.Step(`^the simulated key service becomes unreachable once one keyset has been rewrapped$`, func(ctx context.Context) error {
		w := world(ctx).Secrets
		w.sim.mu.Lock()
		w.sim.failAfterRewraps = 1
		w.sim.mu.Unlock()
		return nil
	})
	sc.Step(`^the simulated key service is reachable again$`, func(ctx context.Context) error {
		w := world(ctx).Secrets
		w.sim.mu.Lock()
		w.sim.unreachable = false
		w.sim.failAfterRewraps = 0
		w.sim.rewraps = 0
		w.sim.mu.Unlock()
		return nil
	})
	sc.Step(`^the simulated key service deletes master key "([^"]*)"$`, func(ctx context.Context, name string) error {
		world(ctx).Secrets.sim.delete(name)
		return nil
	})
	sc.Step(`^a payload codec with an empty keyset store and master key "([^"]*)"$`, newPayloadCodec)
	sc.Step(`^a second payload codec sharing the keyset store and master key "([^"]*)"$`, func(ctx context.Context, mk string) error {
		w := world(ctx).Secrets
		ks := &simKeyService{reg: w.sim, name: mk}
		c, err := secrets.NewCodec(w.store, []secrets.KeyService{ks}, codecOpts(w)...)
		if err != nil {
			return err
		}
		ks.codec = c
		w.codec2 = c
		return nil
	})
	sc.Step(`^the payload codec restarts with the same keyset store and master key "([^"]*)"$`, newPayloadCodec)
	sc.Step(`^the payload codec restarts with the same keyset store and master keys "([^"]*)" and "([^"]*)"$`, newPayloadCodecWithPrev)
	sc.Step(`^the payload codec has restarted with the same keyset store and master key "([^"]*)"$`, newPayloadCodec)
	sc.Step(`^payload "([^"]*)" with encoding "([^"]*)" and data "([^"]*)"$`, payloadGiven)
	sc.Step(`^payload "([^"]*)" with encoding "([^"]*)" and data "\{\\"approved\\":true\}"$`, func(ctx context.Context, name, enc string) error {
		return payloadGiven(ctx, name, enc, `{"approved":true}`)
	})
	sc.Step(`^payload "([^"]*)" with encoding "([^"]*)" and data:$`, payloadGivenDoc)
	sc.Step(`^payload "([^"]*)" with encoding "([^"]*)" and (\d+) random bytes from seed (\d+)$`,
		func(ctx context.Context, name, enc string, size, seed int) error {
			w := world(ctx).Secrets
			w.payloads[name] = secrets.Payload{
				Metadata: map[string][]byte{"encoding": []byte(enc)},
				Data:     bytesFromSeed(int64(seed), size),
			}
			return nil
		})
	sc.Step(`^payload "([^"]*)" with encoding "([^"]*)" and data "([^"]*)" encoded for tenant "([^"]*)"$`, encodePayloadGiven)
	sc.Step(`^payload "([^"]*)" with encoding "([^"]*)" and data "([^"]*)" is encoded for tenant "([^"]*)"$`, encodeWhenEncoded)
	sc.Step(`^each payload codec has encoded a payload for tenant "([^"]*)"$`, eachCodecEncodes)
	sc.Step(`^each payload codec decodes for tenant "([^"]*)" the payload the other one encoded$`, crossCodecDecode)
	sc.Step(`^payload "([^"]*)" is encoded for tenant "([^"]*)"$`, encodePayload)
	sc.Step(`^payload "([^"]*)" is encoded for tenant "([^"]*)" as "([^"]*)"$`, encodePayloadAs)
	sc.Step(`^payload "([^"]*)" is encoded without a tenant$`, encodeNoTenant)
	sc.Step(`^encoded payload "([^"]*)" is decoded for tenant "([^"]*)"$`, decodePayload)
	sc.Step(`^encoded payload "([^"]*)" is decoded without a tenant$`, decodeNoTenant)
	sc.Step(`^encoded payload "([^"]*)" is decoded for tenant "([^"]*)" by the first payload codec$`, decodePayloadCodec1)
	sc.Step(`^encoded payload "([^"]*)" decodes for tenant "([^"]*)" to "([^"]*)"$`, assertDecodedData)
	sc.Step(`^the decoded payload data is "([^"]*)"$`, assertLastDecoded)
	sc.Step(`^decoded payload data is "([^"]*)"$`, assertLastDecoded)
	sc.Step(`^encoding fails with code "([^"]*)"$`, assertLastCode)
	sc.Step(`^decoding fails with code "([^"]*)"$`, assertLastCode)
	sc.Step(`^decoding fails with code "([^"]*)" and message "([^"]*)"$`, assertLastCodeMessage)
	sc.Step(`^encoding fails with code "([^"]*)" and message "([^"]*)"$`, assertLastCodeMessage)
	sc.Step(`^opening fails with code "([^"]*)" and message "(.+)"$`, assertLastCodeMessage)
	sc.Step(`^opening fails with code "([^"]*)"$`, assertLastCode)
	sc.Step(`^the rotation fails with code "([^"]*)"$`, assertLastCode)
	sc.Step(`^rotation fails with code "([^"]*)"$`, assertLastCode)
	sc.Step(`^the data key of tenant "([^"]*)" is rotated$`, rotateDataKey)
	sc.Step(`^the data key of tenant "([^"]*)" has been rotated once since$`, rotateDataKeyOnce)
	sc.Step(`^the second payload codec rotates the data key of tenant "([^"]*)"$`, rotateDataKeyCodec2)
	sc.Step(`^the second payload codec encodes payload "([^"]*)" with encoding "([^"]*)" and data "([^"]*)" for tenant "([^"]*)"$`,
		encodePayloadCodec2)
	sc.Step(`^the master key is rotated from "([^"]*)" to "([^"]*)"$`, rotateMaster)
	sc.Step(`^a master key rotation from "([^"]*)" to "([^"]*)" has failed once$`, masterRotationFailedOnce)
	sc.Step(`^both payload codecs encode a first payload for tenant "([^"]*)" at the same moment$`, concurrentFirstEncode)
	sc.Step(`^the keyset store holds (\d+) keyset for tenant "([^"]*)"$`, assertKeysetCount)
	sc.Step(`^the keyset store holds no keyset for tenant "([^"]*)"$`, assertNoKeyset)
	sc.Step(`^the stored keyset of tenant "([^"]*)" names master key "([^"]*)" as the key that wrapped it$`, assertMasterNames)
	sc.Step(`^the stored keysets of tenants "([^"]*)" and "([^"]*)" name master key "([^"]*)" as the key that wrapped them$`, assertMastersTwo)
	sc.Step(`^the stored keyset of tenant "([^"]*)" is unchanged$`, assertStoredUnchanged)
	sc.Step(`^the stored keyset of tenant "([^"]*)" is replaced by the stored keyset of tenant "([^"]*)"$`, swapKeysets)
	sc.Step(`^the stored keyset of tenant "([^"]*)" contains the bytes of none of its data keys$`, assertNoDataKeyLeaks)
	sc.Step(`^one stored keyset names master key "([^"]*)" and the other names master key "([^"]*)"$`, assertSplitMasters)
	sc.Step(`^encoded payloads "([^"]*)" and "([^"]*)" name different data keys$`, assertDifferentKeys)
	sc.Step(`^encoded payloads "([^"]*)" and "([^"]*)" have different data$`, assertDifferentCipher)
	sc.Step(`^encoded payloads "([^"]*)" and "([^"]*)" both decode for tenant "([^"]*)" to the data of payload "([^"]*)"$`, assertBothDecode)
	sc.Step(`^encoded payloads "([^"]*)" and "([^"]*)" name the same data key$`, assertSameKeys)
	sc.Step(`^encoded payload "([^"]*)" names the primary data key of tenant "([^"]*)"$`, assertNamesPrimary)
	sc.Step(`^encoded payload "([^"]*)" has exactly the metadata keys "([^"]*)" and "([^"]*)"$`, assertMetaKeys)
	sc.Step(`^encoded payload "([^"]*)" has encoding "([^"]*)"$`, assertEncEncoding)
	sc.Step(`^the data of encoded payload "([^"]*)" contains neither "([^"]*)" nor "([^"]*)"$`, assertDataLacks)
	sc.Step(`^the decoded payload has encoding "([^"]*)" and no other metadata$`, assertDecodedMeta)
	sc.Step(`^the decoded payload data equals the data of payload "([^"]*)" byte for byte$`, assertDecodedEqualsPayload)
	sc.Step(`^no encoded payload is returned$`, assertNoEncoded)
	sc.Step(`^no decoded payload is returned$`, assertNoDecoded)
	sc.Step(`^no value is returned$`, assertNoValue)
	sc.Step(`^a new payload is encoded for tenant "([^"]*)"$`, encodeNewPayload)
	sc.Step(`^(\d+) payloads are encoded and decoded for tenant "([^"]*)"$`, benchEncodeDecode)
	sc.Step(`^the simulated key service received (\d+) unwrap request since the restart$`, assertUnwrapSinceRestart)
	sc.Step(`^the first payload codec sent (\d+) unwrap request to the simulated key service for that decode$`, assertCodecUnwrap)
	sc.Step(`^the keyset of tenant "([^"]*)" holds (\d+) data keys$`, assertDataKeyCount)
	sc.Step(`^encoded payload "([^"]*)" is tampered with: (.+)$`, tamperPayload)
	sc.Step(`^decoding encoded payload "([^"]*)" for tenant "([^"]*)" has failed$`, decodePayloadMustFail)
	sc.Step(`^payload "([^"]*)" is decoded for tenant "([^"]*)" without being encoded$`, decodeRaw)
	sc.Step(`^the secrets port is opened with URL "([^"]*)"$`, openSecretsURL)
	sc.Step(`^the secrets port is opened with a local key of 32 random bytes from seed (\d+)$`, openSecretsSeed)
	sc.Step(`^the secrets port is reopened with the same URL$`, reopenSecrets)
	sc.Step(`^the value "([^"]*)" is encrypted$`, encryptValue)
	sc.Step(`^the ciphertext does not contain "([^"]*)"$`, ciphertextLacks)
	sc.Step(`^the ciphertext decrypts to "([^"]*)"$`, decryptCiphertext)
	sc.Step(`^the ciphertext is decrypted after (.+)$`, decryptAfterChange)
	sc.Step(`^decryption fails with code "([^"]*)"$`, assertLastCode)
	sc.Step(`^the log is captured$`, captureLog)
	sc.Step(`^neither the error nor the captured log contains "([^"]*)"$`, logAndErrLack)
	sc.Step(`^neither the error nor the captured log contains the bytes of a data key of tenant "([^"]*)" or of master key "([^"]*)"$`,
		logLacksKeyMaterial)
	sc.Step(`^the captured log does not contain "([^"]*)"$`, logLacks)
	sc.Step(`^the captured log does not contain the bytes of a data key of tenant "([^"]*)" or of master key "([^"]*)"$`, logLacksKeyMaterial)
}

func secretsW(ctx context.Context) *secretsWorld {
	w := world(ctx)
	if w.Secrets == nil {
		w.Secrets = newSecretsWorld()
	}
	if w.Secrets.sim == nil {
		w.Secrets.sim = newSimRegistry()
	}
	return w.Secrets
}

func newPayloadCodec(ctx context.Context, mk string) error {
	sw := secretsW(ctx)
	if sw.store == nil {
		sw.store = newBDDKeysetStore()
	}
	sw.sim.hold(mk)
	ks := &simKeyService{reg: sw.sim, name: mk}
	c, err := secrets.NewCodec(sw.store, []secrets.KeyService{ks}, codecOpts(sw)...)
	if err != nil {
		return err
	}
	ks.codec = c
	sw.codec = c
	sw.unwrapAtRestart = sw.sim.unwrapTotal()
	return nil
}

func newPayloadCodecWithPrev(ctx context.Context, cur, prev string) error {
	sw := secretsW(ctx)
	sw.sim.hold(cur)
	sw.sim.hold(prev)
	curKS := &simKeyService{reg: sw.sim, name: cur}
	prevKS := &simKeyService{reg: sw.sim, name: prev}
	c, err := secrets.NewCodec(sw.store, []secrets.KeyService{curKS, prevKS}, codecOpts(sw)...)
	if err != nil {
		return err
	}
	curKS.codec = c
	sw.codec = c
	sw.unwrapAtRestart = sw.sim.unwrapTotal()
	return nil
}

func codecOpts(sw *secretsWorld) []secrets.Option {
	if sw.logHandler == nil {
		return nil
	}
	return []secrets.Option{secrets.WithLogger(slog.New(sw.logHandler))}
}

func secretsOpts(sw *secretsWorld) []secrets.Option {
	return codecOpts(sw)
}

func snapshotStore(ctx context.Context, sw *secretsWorld, tenant string) error {
	rec, ok, err := sw.store.Get(ctx, tenant)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if sw.storeSnaps == nil {
		sw.storeSnaps = map[string]secrets.StoredKeyset{}
	}
	sw.storeSnaps[tenant] = rec
	return nil
}

func storedKeysetEqual(a, b secrets.StoredKeyset) bool {
	return a.Revision == b.Revision && a.MasterKeyID == b.MasterKeyID && bytes.Equal(a.Wrapped, b.Wrapped)
}

func payloadGiven(ctx context.Context, name, enc, data string) error {
	sw := secretsW(ctx)
	sw.payloads[name] = secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte(enc)},
		Data:     []byte(data),
	}
	return nil
}

func payloadGivenDoc(ctx context.Context, name, enc string, doc *godog.DocString) error {
	sw := secretsW(ctx)
	sw.payloads[name] = secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte(enc)},
		Data:     []byte(strings.TrimSuffix(doc.Content, "\n")),
	}
	return nil
}

func encodePayloadGiven(ctx context.Context, name, enc, data, tenant string) error {
	if err := payloadGiven(ctx, name, enc, data); err != nil {
		return err
	}
	return encodePayloadAs(ctx, name, tenant, name)
}

func encodeWhenEncoded(ctx context.Context, name, enc, data, tenant string) error {
	if err := payloadGiven(ctx, name, enc, data); err != nil {
		return err
	}
	sw := secretsW(ctx)
	p := sw.payloads[name]
	encOut, err := sw.codec.Encode(tenantCtx(ctx, tenant), p)
	sw.lastErr = err
	sw.lastOp = "encode"
	sw.lastEnc = encOut
	if err == nil {
		sw.encoded[name] = encOut
		return snapshotStore(ctx, sw, tenant)
	}
	return nil
}

func encodePayload(ctx context.Context, name, tenant string) error {
	return encodePayloadAs(ctx, name, tenant, name)
}

func encodePayloadAs(ctx context.Context, name, tenant, out string) error {
	sw := secretsW(ctx)
	p, ok := sw.payloads[name]
	if !ok {
		return fmt.Errorf("unknown payload %s", name)
	}
	enc, err := sw.codec.Encode(tenantCtx(ctx, tenant), p)
	sw.lastErr = err
	sw.lastOp = "encode"
	sw.lastEnc = enc
	if err != nil {
		return err
	}
	sw.encoded[out] = enc
	return snapshotStore(ctx, sw, tenant)
}

func encodeNoTenant(ctx context.Context, name string) error {
	sw := secretsW(ctx)
	p := sw.payloads[name]
	enc, err := sw.codec.Encode(ctx, p)
	sw.lastErr = err
	sw.lastOp = "encode"
	sw.lastEnc = enc
	return nil
}

func decodePayload(ctx context.Context, name, tenant string) error {
	sw := secretsW(ctx)
	enc, ok := sw.encoded[name]
	if !ok {
		return fmt.Errorf("unknown encoded payload %s", name)
	}
	out, err := sw.codec.Decode(tenantCtx(ctx, tenant), enc)
	sw.lastErr = err
	sw.lastOp = "decode"
	sw.lastDec = out
	if err != nil {
		return nil
	}
	sw.recordDecoded(name, out)
	return nil
}

func (sw *secretsWorld) recordDecoded(name string, out secrets.Payload) {
	sw.decoded[name] = out
	sw.lastDecodedPayloadName = name
}

func decodePayloadMustFail(ctx context.Context, name, tenant string) error {
	if err := decodePayload(ctx, name, tenant); err != nil {
		return err
	}
	if secretsW(ctx).lastErr == nil {
		return fmt.Errorf("decode succeeded")
	}
	return nil
}

func decodePayloadCodec1(ctx context.Context, name, tenant string) error {
	sw := secretsW(ctx)
	before := sw.sim.unwrapForCodec(sw.codec)
	enc := sw.encoded[name]
	out, err := sw.codec.Decode(tenantCtx(ctx, tenant), enc)
	sw.lastErr = err
	sw.lastOp = "decode"
	sw.lastDec = out
	if err == nil {
		sw.recordDecoded(name, out)
	}
	sw.lastCodecUnwraps = sw.sim.unwrapForCodec(sw.codec) - before
	return nil
}

func decodeNoTenant(ctx context.Context, name string) error {
	sw := secretsW(ctx)
	out, err := sw.codec.Decode(ctx, sw.encoded[name])
	sw.lastErr = err
	sw.lastOp = "decode"
	sw.lastDec = out
	return nil
}

func decodeRaw(ctx context.Context, name, tenant string) error {
	sw := secretsW(ctx)
	out, err := sw.codec.Decode(tenantCtx(ctx, tenant), sw.payloads[name])
	sw.lastErr = err
	sw.lastOp = "decode"
	sw.lastDec = out
	return nil
}

func rotateDataKey(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	sw.lastErr = sw.codec.RotateDataKey(tenantCtx(ctx, tenant))
	sw.lastOp = "rotate"
	return nil
}

func rotateDataKeyOnce(ctx context.Context, tenant string) error {
	return rotateDataKey(ctx, tenant)
}

func rotateDataKeyCodec2(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	return sw.codec2.RotateDataKey(tenantCtx(ctx, tenant))
}

func rotateMaster(ctx context.Context, from, to string) error {
	sw := secretsW(ctx)
	sw.sim.hold(to)
	newKS := &simKeyService{reg: sw.sim, name: to}
	sw.lastErr = sw.codec.RotateMasterKey(ctx, newKS)
	sw.lastOp = "master-rotate"
	return nil
}

func masterRotationFailedOnce(ctx context.Context, from, to string) error {
	sw := secretsW(ctx)
	if sw.codec == nil {
		return fmt.Errorf("codec required")
	}
	sw.sim.hold(to)
	newKS := &simKeyService{reg: sw.sim, name: to}
	sw.lastErr = sw.codec.RotateMasterKey(ctx, newKS)
	sw.lastOp = "master-rotate"
	if sw.lastErr == nil {
		return fmt.Errorf("master rotation should have failed")
	}
	if core.CodeOf(sw.lastErr) != core.CodeUnavailable {
		return fmt.Errorf("master rotation: want unavailable, got %w", sw.lastErr)
	}
	ma, oka, err := sw.store.Get(ctx, "acme")
	if err != nil {
		return err
	}
	mb, okb, err := sw.store.Get(ctx, "globex")
	if err != nil {
		return err
	}
	if !oka || !okb || ma.MasterKeyID == mb.MasterKeyID {
		return fmt.Errorf("expected split master keys after failed rotation")
	}
	return nil
}

func concurrentFirstEncode(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	var wg sync.WaitGroup
	type res struct {
		name string
		out  secrets.Payload
		err  error
	}
	ch := make(chan res, 2)
	encode := func(c *secrets.Codec, tag string) {
		defer wg.Done()
		p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("{" + tag + "}")}
		out, err := c.Encode(tenantCtx(ctx, tenant), p)
		ch <- res{tag, out, err}
	}
	wg.Add(2)
	go encode(sw.codec, "1")
	go encode(sw.codec2, "2")
	wg.Wait()
	close(ch)
	for r := range ch {
		if r.err != nil {
			return r.err
		}
		sw.encoded["race-"+r.name] = r.out
	}
	return nil
}

func encodePayloadCodec2(ctx context.Context, name, enc, data, tenant string) error {
	sw := secretsW(ctx)
	p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(enc)}, Data: []byte(data)}
	encOut, err := sw.codec2.Encode(tenantCtx(ctx, tenant), p)
	if err != nil {
		return err
	}
	sw.encoded[name] = encOut
	return nil
}

func encodeNewPayload(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("x")}
	_, err := sw.codec.Encode(tenantCtx(ctx, tenant), p)
	sw.lastErr = err
	sw.lastOp = "encode"
	return nil
}

func benchEncodeDecode(ctx context.Context, n int, tenant string) error {
	sw := secretsW(ctx)
	for i := 0; i < n; i++ {
		p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("{}")}
		enc, err := sw.codec.Encode(tenantCtx(ctx, tenant), p)
		if err != nil {
			return err
		}
		if _, err := sw.codec.Decode(tenantCtx(ctx, tenant), enc); err != nil {
			return err
		}
	}
	return nil
}

func tamperPayload(ctx context.Context, name, change string) error {
	sw := secretsW(ctx)
	p := sw.encoded[name]
	data := append([]byte(nil), p.Data...)
	switch strings.TrimSpace(change) {
	case "one bit of the ciphertext is flipped":
		if len(data) > 0 {
			data[0] ^= 1
		}
	case "the last byte of the ciphertext is removed":
		if len(data) > 0 {
			data = data[:len(data)-1]
		}
	case "the ciphertext is cut to its first 10 bytes":
		if len(data) > 10 {
			data = data[:10]
		}
	case "the ciphertext is emptied":
		data = nil
	case "the data key id is removed":
		p.Metadata = maps.Clone(p.Metadata)
		delete(p.Metadata, secrets.MetaEncryptionKeyID)
		sw.encoded[name] = p
		return nil
	case "the data key id is replaced by an id no keyset holds":
		p.Metadata = maps.Clone(p.Metadata)
		p.Metadata[secrets.MetaEncryptionKeyID] = []byte("999999999")
		sw.encoded[name] = p
		return nil
	default:
		if strings.Contains(change, "primary data key id of") {
			m := regexp.MustCompile(`"([^"]+)"`)
			t := m.FindStringSubmatch(change)[1]
			id, err := sw.codec.PrimaryDataKeyID(tenantCtx(ctx, t))
			if err != nil {
				return err
			}
			p.Metadata = maps.Clone(p.Metadata)
			p.Metadata[secrets.MetaEncryptionKeyID] = []byte(id)
			sw.encoded[name] = p
			return nil
		}
		return fmt.Errorf("unknown tamper: %s", change)
	}
	p.Data = data
	sw.encoded[name] = p
	return nil
}

func assertKeysetCount(ctx context.Context, n int, tenant string) error {
	sw := secretsW(ctx)
	rec, ok, err := sw.store.Get(ctx, tenant)
	if err != nil {
		return err
	}
	if (n > 0) != ok {
		return fmt.Errorf("keyset presence = %v, want %d", ok, n)
	}
	if n > 0 && len(rec.Wrapped) == 0 {
		return fmt.Errorf("empty wrapped keyset")
	}
	cnt, err := sw.store.Count(ctx)
	if err != nil {
		return err
	}
	if n == 1 && cnt < 1 {
		return fmt.Errorf("store count %d", cnt)
	}
	return nil
}

func assertNoKeyset(ctx context.Context, tenant string) error {
	_, ok, err := secretsW(ctx).store.Get(ctx, tenant)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("keyset exists for %s", tenant)
	}
	return nil
}

func assertMasterNames(ctx context.Context, tenant, mk string) error {
	rec, ok, err := secretsW(ctx).store.Get(ctx, tenant)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no keyset for %s", tenant)
	}
	if rec.MasterKeyID != mk {
		return fmt.Errorf("master %q want %q", rec.MasterKeyID, mk)
	}
	return nil
}

func assertMastersTwo(ctx context.Context, a, b, mk string) error {
	if err := assertMasterNames(ctx, a, mk); err != nil {
		return err
	}
	return assertMasterNames(ctx, b, mk)
}

func assertStoredUnchanged(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	snap, ok := sw.storeSnaps[tenant]
	if !ok {
		return fmt.Errorf("no snapshot for tenant %q", tenant)
	}
	cur, found, err := sw.store.Get(ctx, tenant)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("keyset missing after failed op")
	}
	if !storedKeysetEqual(snap, cur) {
		return fmt.Errorf("stored keyset changed for %q", tenant)
	}
	return nil
}

func swapKeysets(ctx context.Context, a, b string) error {
	sw := secretsW(ctx)
	ra, oka, _ := sw.store.Get(ctx, a)
	rb, okb, _ := sw.store.Get(ctx, b)
	if !oka || !okb {
		return fmt.Errorf("missing keysets")
	}
	st, ok := sw.store.(*bddKeysetStore)
	if !ok {
		return fmt.Errorf("store cannot replace records")
	}
	if err := st.Replace(a, rb); err != nil {
		return err
	}
	return st.Replace(b, ra)
}

func assertNoDataKeyLeaks(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	rec, ok, err := sw.store.Get(ctx, tenant)
	if err != nil || !ok {
		return err
	}
	plain := sw.sim.keysetPlain(tenant)
	if len(plain) == 0 {
		return fmt.Errorf("no data key material recorded for unwrap")
	}
	blobs, err := dataKeyMaterialBytes(plain)
	if err != nil {
		return err
	}
	if len(blobs) == 0 {
		return fmt.Errorf("no data key material bytes parsed from keyset")
	}
	for _, b := range blobs {
		if len(b) > 0 && bytes.Contains(rec.Wrapped, b) {
			return fmt.Errorf("wrapped keyset contains data key bytes")
		}
	}
	return nil
}

func assertSplitMasters(ctx context.Context, a, b string) error {
	sw := secretsW(ctx)
	ma, oka, err := sw.store.Get(ctx, "acme")
	if err != nil {
		return err
	}
	mb, okb, err := sw.store.Get(ctx, "globex")
	if err != nil {
		return err
	}
	if !oka || !okb {
		return fmt.Errorf("missing keysets")
	}
	ids := map[string]bool{ma.MasterKeyID: true, mb.MasterKeyID: true}
	if !ids[a] || !ids[b] || ma.MasterKeyID == mb.MasterKeyID {
		return fmt.Errorf("masters %q %q want distinct %q %q", ma.MasterKeyID, mb.MasterKeyID, a, b)
	}
	return nil
}

func keyIDOf(sw *secretsWorld, name string) string {
	return string(sw.encoded[name].Metadata[secrets.MetaEncryptionKeyID])
}

func assertDifferentKeys(ctx context.Context, a, b string) error {
	sw := secretsW(ctx)
	if keyIDOf(sw, a) == keyIDOf(sw, b) {
		return fmt.Errorf("same key id")
	}
	return nil
}

func assertSameKeys(ctx context.Context, a, b string) error {
	sw := secretsW(ctx)
	if keyIDOf(sw, a) != keyIDOf(sw, b) {
		return fmt.Errorf("different key ids %q %q", keyIDOf(sw, a), keyIDOf(sw, b))
	}
	return nil
}

func assertDifferentCipher(ctx context.Context, a, b string) error {
	sw := secretsW(ctx)
	if bytes.Equal(sw.encoded[a].Data, sw.encoded[b].Data) {
		return fmt.Errorf("same ciphertext")
	}
	return nil
}

func assertBothDecode(ctx context.Context, a, b, tenant, src string) error {
	for _, id := range []string{a, b} {
		if err := decodePayload(ctx, id, tenant); err != nil {
			return err
		}
		if !bytes.Equal(swDecoded(ctx, id).Data, secretsW(ctx).payloads[src].Data) {
			return fmt.Errorf("decode mismatch for %s", id)
		}
	}
	return nil
}

func swDecoded(ctx context.Context, name string) secrets.Payload {
	return secretsW(ctx).decoded[name]
}

func assertNamesPrimary(ctx context.Context, name, tenant string) error {
	sw := secretsW(ctx)
	want, err := sw.codec.PrimaryDataKeyID(tenantCtx(ctx, tenant))
	if err != nil {
		return err
	}
	got := keyIDOf(sw, name)
	if got != want {
		return fmt.Errorf("key id %q want primary %q", got, want)
	}
	return nil
}

func assertMetaKeys(ctx context.Context, name, k1, k2 string) error {
	sw := secretsW(ctx)
	keys := slices.Sorted(maps.Keys(sw.encoded[name].Metadata))
	want := []string{k1, k2}
	slices.Sort(want)
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		return fmt.Errorf("metadata keys %v want %v", keys, want)
	}
	return nil
}

func assertEncEncoding(ctx context.Context, name, enc string) error {
	if string(secretsW(ctx).encoded[name].Metadata["encoding"]) != enc {
		return fmt.Errorf("wrong encoding")
	}
	return nil
}

func assertDataLacks(ctx context.Context, name, a, b string) error {
	d := secretsW(ctx).encoded[name].Data
	if bytes.Contains(d, []byte(a)) || bytes.Contains(d, []byte(b)) {
		return fmt.Errorf("data leaks plaintext")
	}
	return nil
}

func assertDecodedMeta(ctx context.Context, enc string) error {
	p := swDecoded(ctx, lastDecodedName(ctx))
	if len(p.Metadata) != 1 || string(p.Metadata["encoding"]) != enc {
		return fmt.Errorf("metadata %+v", p.Metadata)
	}
	return nil
}

func lastDecodedName(ctx context.Context) string {
	return secretsW(ctx).lastDecodedPayloadName
}

func assertDecodedEqualsPayload(ctx context.Context, name string) error {
	d := swDecoded(ctx, name)
	if d.Data == nil {
		d = swDecoded(ctx, lastDecodedName(ctx))
	}
	want := secretsW(ctx).payloads[name]
	if !bytes.Equal(d.Data, want.Data) {
		return fmt.Errorf("data mismatch")
	}
	return nil
}

func assertDecodedData(ctx context.Context, encName, tenant, data string) error {
	if err := decodePayload(ctx, encName, tenant); err != nil {
		return err
	}
	if string(swDecoded(ctx, encName).Data) != data {
		return fmt.Errorf("got %q", swDecoded(ctx, encName).Data)
	}
	return nil
}

func assertLastDecoded(ctx context.Context, data string) error {
	name := lastDecodedName(ctx)
	if name == "" {
		return fmt.Errorf("no decoded payload matching %q", data)
	}
	p := swDecoded(ctx, name)
	if string(p.Data) != data {
		return fmt.Errorf("got %q", p.Data)
	}
	return nil
}

func assertLastCode(ctx context.Context, code string) error {
	sw := secretsW(ctx)
	if sw.lastErr == nil {
		return fmt.Errorf("succeeded, want %s", code)
	}
	if string(core.CodeOf(sw.lastErr)) != code {
		return fmt.Errorf("code %s want %s", core.CodeOf(sw.lastErr), code)
	}
	return nil
}

func assertLastCodeMessage(ctx context.Context, code, msg string) error {
	sw := secretsW(ctx)
	if sw.lastErr == nil {
		return fmt.Errorf("succeeded, want %s", code)
	}
	var pe *core.Error
	if errors.As(sw.lastErr, &pe) {
		if string(pe.Code) != code || pe.Message != msg {
			return fmt.Errorf("got %s: %q", pe.Code, pe.Message)
		}
		return nil
	}
	return fmt.Errorf("not a core error")
}

func payloadIsZero(p secrets.Payload) bool {
	return len(p.Data) == 0 && len(p.Metadata) == 0
}

func assertNoEncoded(ctx context.Context) error {
	sw := secretsW(ctx)
	if sw.lastErr == nil {
		return fmt.Errorf("encode succeeded")
	}
	if !payloadIsZero(sw.lastEnc) {
		return fmt.Errorf("encoded payload returned")
	}
	return nil
}

func assertNoDecoded(ctx context.Context) error {
	sw := secretsW(ctx)
	if sw.lastErr == nil {
		return fmt.Errorf("decode succeeded")
	}
	if !payloadIsZero(sw.lastDec) {
		return fmt.Errorf("decoded payload returned")
	}
	return nil
}

func assertNoValue(ctx context.Context) error {
	sw := secretsW(ctx)
	if sw.lastErr == nil {
		return fmt.Errorf("value returned without error")
	}
	if len(sw.lastVal) != 0 {
		return fmt.Errorf("value bytes returned with error")
	}
	return nil
}

func assertUnwrapSinceRestart(ctx context.Context, n int) error {
	sw := secretsW(ctx)
	got := sw.sim.unwrapTotal() - sw.unwrapAtRestart
	if got != n {
		return fmt.Errorf("unwraps %d want %d", got, n)
	}
	return nil
}

func assertCodecUnwrap(ctx context.Context, n int) error {
	if secretsW(ctx).lastCodecUnwraps != n {
		return fmt.Errorf("codec unwraps %d want %d", secretsW(ctx).lastCodecUnwraps, n)
	}
	return nil
}

func assertDataKeyCount(ctx context.Context, tenant string, n int) error {
	cnt, err := secretsW(ctx).codec.DataKeyCount(tenantCtx(ctx, tenant))
	if err != nil {
		return err
	}
	if cnt != n {
		return fmt.Errorf("data keys %d want %d", cnt, n)
	}
	return nil
}

func openSecretsURL(ctx context.Context, raw string) error {
	sw := secretsW(ctx)
	p, err := secrets.OpenURL(ctx, raw, secretsOpts(sw)...)
	sw.lastErr = err
	sw.lastOp = "open"
	if err == nil {
		sw.port = p
		sw.portURL = raw
	}
	return nil
}

func openSecretsSeed(ctx context.Context, seed int) error {
	return openSecretsURL(ctx, localKeyURLFromSeed(int64(seed)))
}

func reopenSecrets(ctx context.Context) error {
	return openSecretsURL(ctx, secretsW(ctx).portURL)
}

func encryptValue(ctx context.Context, v string) error {
	sw := secretsW(ctx)
	sw.valuePlain = []byte(v)
	ct, err := sw.port.Encrypt(ctx, sw.valuePlain)
	sw.lastErr = err
	sw.lastVal = ct
	sw.ciphertext = ct
	return nil
}

func ciphertextLacks(ctx context.Context, sub string) error {
	if bytes.Contains(secretsW(ctx).ciphertext, []byte(sub)) {
		return fmt.Errorf("ciphertext contains %q", sub)
	}
	return nil
}

func decryptCiphertext(ctx context.Context, want string) error {
	sw := secretsW(ctx)
	pt, err := sw.port.Decrypt(ctx, sw.ciphertext)
	sw.lastVal = pt
	sw.lastErr = err
	if err != nil {
		return err
	}
	if string(pt) != want {
		return fmt.Errorf("decrypt %q want %q", pt, want)
	}
	return nil
}

func decryptAfterChange(ctx context.Context, change string) error {
	sw := secretsW(ctx)
	change = strings.TrimSpace(change)
	ct := append([]byte(nil), sw.ciphertext...)
	switch change {
	case "flipping one bit of the ciphertext":
		if len(ct) > 0 {
			ct[0] ^= 1
		}
	case "cutting the ciphertext to its first 10 bytes":
		ct = ct[:min(10, len(ct))]
	case "reopening the secrets port with a local key from seed 4":
		if err := openSecretsSeed(ctx, 4); err != nil {
			return err
		}
		pt, err := sw.port.Decrypt(ctx, sw.ciphertext)
		sw.lastErr = err
		sw.lastVal = pt
		return nil
	default:
		if strings.Contains(change, "seed 4") {
			if err := openSecretsSeed(ctx, 4); err != nil {
				return err
			}
			pt, err := sw.port.Decrypt(ctx, sw.ciphertext)
			sw.lastErr = err
			sw.lastVal = pt
			return nil
		}
	}
	pt, err := sw.port.Decrypt(ctx, ct)
	sw.lastErr = err
	sw.lastVal = pt
	return nil
}

func captureLog(ctx context.Context) error {
	sw := secretsW(ctx)
	if sw.logBuf != nil {
		sw.logBuf.Reset()
	}
	return nil
}

func logAndErrLack(ctx context.Context, sub string) error {
	return logLacks(ctx, sub)
}

func logLacks(ctx context.Context, sub string) error {
	sw := secretsW(ctx)
	text := sw.logBuf.String()
	if sw.lastErr != nil && strings.Contains(sw.lastErr.Error(), sub) {
		return fmt.Errorf("error contains %q", sub)
	}
	if strings.Contains(text, sub) {
		return fmt.Errorf("log contains %q", sub)
	}
	return nil
}

func logLacksKeyMaterial(ctx context.Context, tenant, mk string) error {
	sw := secretsW(ctx)
	text := sw.logBuf.String()
	if sw.lastErr != nil {
		text += sw.lastErr.Error()
	}
	plain := sw.sim.keysetPlain(tenant)
	if len(plain) == 0 {
		return fmt.Errorf("no data key material to search logs for")
	}
	blobs, err := dataKeyMaterialBytes(plain)
	if err != nil {
		return err
	}
	if len(blobs) == 0 {
		return fmt.Errorf("no data key material bytes parsed from keyset")
	}
	for _, b := range blobs {
		if len(b) > 0 && strings.Contains(text, string(b)) {
			return fmt.Errorf("log/error contains data key bytes")
		}
	}
	mkBytes := sw.sim.keyBytes(mk)
	if strings.Contains(text, string(mkBytes)) {
		return fmt.Errorf("log contains master key bytes")
	}
	return nil
}

func eachCodecEncodes(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	p1, err := sw.codec.Encode(tenantCtx(ctx, tenant), secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte("c1"),
	})
	if err != nil {
		return err
	}
	p2, err := sw.codec2.Encode(tenantCtx(ctx, tenant), secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte("c2"),
	})
	if err != nil {
		return err
	}
	sw.encoded["codec1"] = p1
	sw.encoded["codec2"] = p2
	return nil
}

func crossCodecDecode(ctx context.Context, tenant string) error {
	sw := secretsW(ctx)
	enc1 := sw.encoded["race-1"]
	enc2 := sw.encoded["race-2"]
	out1, err := sw.codec.Decode(tenantCtx(ctx, tenant), enc2)
	if err != nil {
		return err
	}
	out2, err := sw.codec2.Decode(tenantCtx(ctx, tenant), enc1)
	if err != nil {
		return err
	}
	if string(out1.Data) != "{2}" || string(out2.Data) != "{1}" {
		return fmt.Errorf("cross decode got %q and %q", out1.Data, out2.Data)
	}
	return nil
}
