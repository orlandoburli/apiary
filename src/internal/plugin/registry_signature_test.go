package plugin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// signingKey assembles minisign-format keys and signatures byte for byte, so the
// verifier is exercised against the real layout rather than against a shape this
// package invented for itself.
type signingKey struct {
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	keyID   [8]byte
}

func newSigningKey(t *testing.T) *signingKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := &signingKey{public: public, private: private}
	if _, err := rand.Read(key.keyID[:]); err != nil {
		t.Fatal(err)
	}
	return key
}

func (k *signingKey) publicKeyLine() string {
	raw := append([]byte(minisignAlgorithmLegacy), k.keyID[:]...)
	raw = append(raw, k.public...)
	return base64.StdEncoding.EncodeToString(raw)
}

func (k *signingKey) publicKeyFile() string {
	return "untrusted comment: minisign public key " + k.ID() + "\n" + k.publicKeyLine() + "\n"
}

func (k *signingKey) ID() string {
	return (&PublicKey{KeyID: k.keyID}).ID()
}

// sign produces a .minisig, in either the legacy or the prehashed mode minisign
// itself chooses between.
func (k *signingKey) sign(payload []byte, algorithm, trustedComment string) []byte {
	signed := payload
	if algorithm == minisignAlgorithmPrehashed {
		digest := blake2b.Sum512(payload)
		signed = digest[:]
	}
	value := ed25519.Sign(k.private, signed)
	raw := append([]byte(algorithm), k.keyID[:]...)
	raw = append(raw, value...)
	global := ed25519.Sign(k.private, append(append([]byte{}, value...), []byte(trustedComment)...))
	return []byte(fmt.Sprintf("untrusted comment: signature from apiary registry\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(raw), trustedComment, base64.StdEncoding.EncodeToString(global)))
}

func TestVerifySignatureAcceptsBothMinisignModes(t *testing.T) {
	key := newSigningKey(t)
	parsed, err := ParsePublicKey(key.publicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"schema_version":1,"plugins":[]}`)
	for _, algorithm := range []string{minisignAlgorithmLegacy, minisignAlgorithmPrehashed} {
		t.Run(algorithm, func(t *testing.T) {
			if err := VerifySignature(parsed, payload, key.sign(payload, algorithm, "timestamp:1")); err != nil {
				t.Fatalf("a valid %s signature must verify: %v", algorithm, err)
			}
		})
	}
}

func TestParsePublicKeyAcceptsTheBareLine(t *testing.T) {
	key := newSigningKey(t)
	fromFile, err := ParsePublicKey(key.publicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	fromLine, err := ParsePublicKey(key.publicKeyLine())
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.ID() != fromLine.ID() || !fromFile.Key.Equal(fromLine.Key) {
		t.Fatal("a bare key line and a key file must parse to the same key")
	}
}

func TestParsePublicKeyRejectsGarbage(t *testing.T) {
	for _, testCase := range []struct{ name, raw string }{
		{"empty", ""},
		{"not base64", "untrusted comment: x\nnot base64!!"},
		{"wrong length", "AAAA"},
		{"comment only", "untrusted comment: minisign public key"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ParsePublicKey(testCase.raw); err == nil {
				t.Fatal("expected a rejection")
			}
		})
	}
}

func TestVerifySignatureRejectsTamperedContent(t *testing.T) {
	key := newSigningKey(t)
	parsed, _ := ParsePublicKey(key.publicKeyFile())
	payload := []byte(`{"schema_version":1,"plugins":[]}`)
	raw := key.sign(payload, minisignAlgorithmPrehashed, "timestamp:1")
	if err := VerifySignature(parsed, []byte(`{"schema_version":1,"plugins":["evil"]}`), raw); err == nil {
		t.Fatal("a modified index must not verify")
	}
}

// A signature from some other key is not "unsigned", it is wrong.
func TestVerifySignatureRejectsAnotherKey(t *testing.T) {
	signer, pinned := newSigningKey(t), newSigningKey(t)
	parsed, _ := ParsePublicKey(pinned.publicKeyFile())
	payload := []byte("index")
	err := VerifySignature(parsed, payload, signer.sign(payload, minisignAlgorithmLegacy, "t"))
	if err == nil || !strings.Contains(err.Error(), "pinned to key") {
		t.Fatalf("want a key-id mismatch, got %v", err)
	}
}

// The trusted comment is only trustworthy because of the global signature.
func TestVerifySignatureRejectsARewrittenTrustedComment(t *testing.T) {
	key := newSigningKey(t)
	parsed, _ := ParsePublicKey(key.publicKeyFile())
	payload := []byte("index")
	raw := string(key.sign(payload, minisignAlgorithmLegacy, "timestamp:1 file:index.json"))
	tampered := strings.Replace(raw, "trusted comment: timestamp:1 file:index.json", "trusted comment: timestamp:9999 file:other.json", 1)
	err := VerifySignature(parsed, payload, []byte(tampered))
	if err == nil || !strings.Contains(err.Error(), "trusted comment") {
		t.Fatalf("want a trusted-comment rejection, got %v", err)
	}
}

func TestVerifySignatureRejectsMalformedAndMissingSignatures(t *testing.T) {
	key := newSigningKey(t)
	parsed, _ := ParsePublicKey(key.publicKeyFile())
	for _, testCase := range []struct{ name, raw string }{
		{"empty", ""},
		{"comment only", "untrusted comment: nothing here\n"},
		{"not base64", "untrusted comment: x\n!!!not base64!!!\n"},
		{"truncated", "untrusted comment: x\n" + base64.StdEncoding.EncodeToString([]byte("short")) + "\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := VerifySignature(parsed, []byte("index"), []byte(testCase.raw)); err == nil {
				t.Fatal("expected a rejection")
			}
		})
	}
	if err := VerifySignature(nil, []byte("index"), []byte("whatever")); err == nil {
		t.Fatal("verifying without a key must be an error, never a pass")
	}
}

// An unknown algorithm must fail closed rather than fall through to a default.
func TestVerifySignatureRejectsUnknownAlgorithm(t *testing.T) {
	key := newSigningKey(t)
	parsed, _ := ParsePublicKey(key.publicKeyFile())
	payload := []byte("index")
	raw := key.sign(payload, "XX", "t")
	if err := VerifySignature(parsed, payload, raw); err == nil || !strings.Contains(err.Error(), "unsupported signature algorithm") {
		t.Fatalf("want an unsupported-algorithm rejection, got %v", err)
	}
}
