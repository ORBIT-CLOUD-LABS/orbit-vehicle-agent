package firmwaresignature

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:embed public_keys.json
var embeddedPublicKeys []byte

// ErrUnknownKey는 검증기에 요청한 keyId가 내장되어 있지 않음을 나타낸다.
var ErrUnknownKey = errors.New("unknown keyId")

// Metadata는 서명 대상인 차종, 대상 버전과 실제 펌웨어의 해시·크기다.
type Metadata struct {
	Model   string
	Version string
	SHA256  string
	Size    int64
}

// Verifier는 바이너리에 내장된 공개 키로 펌웨어 서명을 검증한다.
type Verifier struct {
	publicKeys map[string]ed25519.PublicKey
}

// New는 내장된 공개 키를 읽어 검증기를 만든다.
func New() (*Verifier, error) {
	return newVerifier(embeddedPublicKeys)
}

func newVerifier(data []byte) (*Verifier, error) {
	var encodedKeys map[string]string
	if err := json.Unmarshal(data, &encodedKeys); err != nil {
		return nil, fmt.Errorf("decode firmware public keys: %w", err)
	}

	publicKeys := make(map[string]ed25519.PublicKey, len(encodedKeys))
	for keyID, encoded := range encodedKeys {
		if keyID == "" {
			return nil, errors.New("firmware public key has empty keyId")
		}
		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode firmware public key %q: %w", keyID, err)
		}
		if len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid firmware public key length for keyId %q: %d", keyID, len(key))
		}
		publicKeys[keyID] = ed25519.PublicKey(key)
	}
	return &Verifier{publicKeys: publicKeys}, nil
}

// HasKey는 keyID에 해당하는 공개 키가 내장되어 있는지 반환한다.
func (v *Verifier) HasKey(keyID string) bool {
	_, ok := v.publicKeys[keyID]
	return ok
}

// Verify는 매니페스트의 keyId에 해당하는 키로 Base64 서명을 검증한다.
func (v *Verifier) Verify(keyID, signature string, metadata Metadata) error {
	if keyID == "" {
		return errors.New("missing keyId")
	}
	key, ok := v.publicKeys[keyID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKey, keyID)
	}

	decoded, err := base64.StdEncoding.Strict().DecodeString(signature)
	if err != nil {
		return fmt.Errorf("decode firmware signature: %w", err)
	}
	if len(decoded) != ed25519.SignatureSize {
		return fmt.Errorf("invalid firmware signature length: %d", len(decoded))
	}

	// 차종과 버전은 각각 한 줄이며, 버전 문자열은 받은 그대로 사용한다.
	if strings.Contains(metadata.Model, "\n") || strings.Contains(metadata.Version, "\n") {
		return errors.New("firmware model or version contains a newline")
	}
	hash, err := hex.DecodeString(metadata.SHA256)
	if err != nil {
		return fmt.Errorf("decode firmware SHA-256: %w", err)
	}
	if len(hash) != sha256.Size {
		return fmt.Errorf("invalid firmware SHA-256 length: %d", len(hash))
	}
	if metadata.Size < 0 {
		return fmt.Errorf("invalid firmware size: %d", metadata.Size)
	}

	payload := fmt.Sprintf("orbit-firmware-v1\n%s\n%s\n%x\n%d", metadata.Model, metadata.Version, hash, metadata.Size)
	if !ed25519.Verify(key, []byte(payload), decoded) {
		return errors.New("firmware signature verification failed")
	}
	return nil
}
