package firmwaresignature

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifier_Verify(t *testing.T) {
	publicV1, privateV1, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	publicV2, privateV2, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keys, err := json.Marshal(map[string][]byte{"test-v1": publicV1, "test-v2": publicV2})
	require.NoError(t, err)
	verifier, err := newVerifier(keys)
	require.NoError(t, err)

	const hash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	const payload = "orbit-firmware-v1\nmodel-a\n1.0\n" + hash + "\n3"
	encode := base64.StdEncoding.EncodeToString
	signatureV1 := encode(ed25519.Sign(privateV1, []byte(payload)))
	signatureV2 := encode(ed25519.Sign(privateV2, []byte(payload)))
	metadata := Metadata{Model: "model-a", Version: "1.0", SHA256: hash, Size: 3}

	tests := []struct {
		name      string
		keyID     string
		signature string
		metadata  Metadata
		wantErr   string
	}{
		{"valid", "test-v1", signatureV1, metadata, ""},
		{"select second key", "test-v2", signatureV2, metadata, ""},
		{"uppercase hash is encoded in lowercase", "test-v1", signatureV1, Metadata{"model-a", "1.0", strings.ToUpper(hash), 3}, ""},
		{"different model", "test-v1", signatureV1, Metadata{"model-b", "1.0", hash, 3}, "firmware signature verification failed"},
		{"different version spelling", "test-v1", signatureV1, Metadata{"model-a", "1.0.0", hash, 3}, "firmware signature verification failed"},
		{"changed file hash", "test-v1", signatureV1, Metadata{"model-a", "1.0", strings.Repeat("0", 64), 3}, "firmware signature verification failed"},
		{"changed file size", "test-v1", signatureV1, Metadata{"model-a", "1.0", hash, 4}, "firmware signature verification failed"},
		{"wrong key", "test-v2", signatureV1, metadata, "firmware signature verification failed"},
		{"trailing newline in signed payload", "test-v1", encode(ed25519.Sign(privateV1, []byte(payload+"\n"))), metadata, "firmware signature verification failed"},
		{"unknown key", "v3", signatureV1, metadata, "unknown keyId: v3"},
		{"missing key", "", signatureV1, metadata, "missing keyId"},
		{"malformed base64", "test-v1", "!", metadata, "decode firmware signature"},
		{"short signature", "test-v1", "AA==", metadata, "invalid firmware signature length"},
		{"missing signature", "test-v1", "", metadata, "invalid firmware signature length"},
		{"multiline model", "test-v1", signatureV1, Metadata{"model\na", "1.0", hash, 3}, "contains a newline"},
		{"multiline version", "test-v1", signatureV1, Metadata{"model-a", "1\n0", hash, 3}, "contains a newline"},
		{"non-hex hash", "test-v1", signatureV1, Metadata{"model-a", "1.0", "zz", 3}, "decode firmware SHA-256"},
		{"short hash", "test-v1", signatureV1, Metadata{"model-a", "1.0", "00", 3}, "invalid firmware SHA-256 length"},
		{"negative size", "test-v1", signatureV1, Metadata{"model-a", "1.0", hash, -1}, "invalid firmware size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifier.Verify(tt.keyID, tt.signature, tt.metadata)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNewVerifier_InvalidPublicKeys(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"invalid JSON", "{", "decode firmware public keys"},
		{"empty key id", `{"":"AA=="}`, "empty keyId"},
		{"invalid base64", `{"v1":"!"}`, `decode firmware public key "v1"`},
		{"short public key", `{"v1":"AA=="}`, "invalid firmware public key length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newVerifier([]byte(tt.data))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNew_EmbeddedPublicKeys(t *testing.T) {
	verifier, err := New()
	require.NoError(t, err)
	for _, keyID := range []string{"test-v1", "test-v2"} {
		assert.ErrorContains(t, verifier.Verify(keyID, "", Metadata{}), "unknown keyId: "+keyID)
	}
}
