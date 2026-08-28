package plugin

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Signature verification covers the index, which carries the digests, which
// cover the artifacts. It does not authenticate plugin publishers: signing the
// artifacts themselves is a separate problem, and this is deliberately not
// presented as solving it.
//
// The format is minisign's, so an operator can check the same file with the
// standard tool and get the same answer:
//
//	minisign -Vm index.json -P <public key>
//
// Public key line:  base64( "Ed" ‖ key_id[8] ‖ ed25519_public_key[32] )
// Signature file:   untrusted comment / base64( algorithm[2] ‖ key_id[8] ‖ signature[64] )
//
//	trusted comment   / base64( global_signature[64] )
//
// The global signature covers signature ‖ trusted_comment, which is what stops
// the trusted comment from being rewritten independently of the payload.
const (
	minisignAlgorithmLegacy    = "Ed" // signature over the file content
	minisignAlgorithmPrehashed = "ED" // signature over BLAKE2b-512 of the content

	publicKeyBytes = 2 + 8 + ed25519.PublicKeySize
	signatureBytes = 2 + 8 + ed25519.SignatureSize
)

// PublicKey is a parsed minisign public key.
type PublicKey struct {
	KeyID [8]byte
	Key   ed25519.PublicKey
}

// ID renders the key id the way minisign prints it.
func (k *PublicKey) ID() string {
	return strings.ToUpper(hex.EncodeToString(k.KeyID[:]))
}

// ParsePublicKey accepts either a full minisign public key file (with its
// comment line) or the bare base64 line, which is what fits in a config file.
func ParsePublicKey(raw string) (*PublicKey, error) {
	line := ""
	for _, candidate := range strings.Split(strings.TrimSpace(raw), "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.HasPrefix(candidate, "untrusted comment:") {
			continue
		}
		line = candidate
		break
	}
	if line == "" {
		return nil, fmt.Errorf("public key is empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return nil, fmt.Errorf("public key is not valid base64: %w", err)
	}
	if len(decoded) != publicKeyBytes {
		return nil, fmt.Errorf("public key is %d bytes, expected %d (a minisign public key)", len(decoded), publicKeyBytes)
	}
	if algorithm := string(decoded[:2]); algorithm != minisignAlgorithmLegacy {
		return nil, fmt.Errorf("unsupported public key algorithm %q; expected %q (ed25519)", algorithm, minisignAlgorithmLegacy)
	}
	key := &PublicKey{Key: ed25519.PublicKey(decoded[10:])}
	copy(key.KeyID[:], decoded[2:10])
	return key, nil
}

// signature is a parsed .minisig file.
type signature struct {
	algorithm      string
	keyID          [8]byte
	value          []byte
	trustedComment string
	globalValue    []byte
}

func parseSignature(raw []byte) (*signature, error) {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var payload, trustedComment, globalLine string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "untrusted comment:"):
			continue
		case strings.HasPrefix(trimmed, "trusted comment:"):
			trustedComment = strings.TrimSpace(strings.TrimPrefix(trimmed, "trusted comment:"))
		case payload == "":
			payload = trimmed
		case globalLine == "":
			globalLine = trimmed
		}
	}
	if payload == "" {
		return nil, fmt.Errorf("signature file carries no signature line")
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(decoded) != signatureBytes {
		return nil, fmt.Errorf("signature is %d bytes, expected %d", len(decoded), signatureBytes)
	}
	parsed := &signature{
		algorithm:      string(decoded[:2]),
		value:          decoded[10:],
		trustedComment: trustedComment,
	}
	copy(parsed.keyID[:], decoded[2:10])
	if globalLine != "" {
		if parsed.globalValue, err = base64.StdEncoding.DecodeString(globalLine); err != nil {
			return nil, fmt.Errorf("global signature is not valid base64: %w", err)
		}
	}
	return parsed, nil
}

// VerifySignature checks a payload against a minisign signature. Every failure
// is fatal to the caller: an index whose signature does not verify is not a
// degraded index, it is an index of unknown origin.
func VerifySignature(key *PublicKey, payload, rawSignature []byte) error {
	if key == nil {
		return fmt.Errorf("no public key to verify against")
	}
	parsed, err := parseSignature(rawSignature)
	if err != nil {
		return err
	}
	if parsed.keyID != key.KeyID {
		return fmt.Errorf("signature was made with key %s, but this registry is pinned to key %s",
			strings.ToUpper(hex.EncodeToString(parsed.keyID[:])), key.ID())
	}
	var signed []byte
	switch parsed.algorithm {
	case minisignAlgorithmLegacy:
		signed = payload
	case minisignAlgorithmPrehashed:
		digest := blake2b.Sum512(payload)
		signed = digest[:]
	default:
		return fmt.Errorf("unsupported signature algorithm %q", parsed.algorithm)
	}
	if !ed25519.Verify(key.Key, signed, parsed.value) {
		return fmt.Errorf("signature does not match the content it accompanies")
	}
	if len(parsed.globalValue) > 0 {
		// The trusted comment is only trustworthy because of this second
		// signature; skipping it would let anyone rewrite what it claims.
		var global bytes.Buffer
		global.Write(parsed.value)
		global.WriteString(parsed.trustedComment)
		if !ed25519.Verify(key.Key, global.Bytes(), parsed.globalValue) {
			return fmt.Errorf("the signature's trusted comment does not verify")
		}
	}
	return nil
}
