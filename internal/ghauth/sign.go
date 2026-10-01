package ghauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const signatureVersion = "noctra-auth-v1"

const (
	headerInstance  = "X-Noctra-Instance"
	headerTimestamp = "X-Noctra-Timestamp"
	headerNonce     = "X-Noctra-Nonce"
	headerSignature = "X-Noctra-Signature"
)

func CanonicalString(method, path string, timestamp int64, nonce, instanceID string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		signatureVersion,
		strings.ToUpper(method),
		path,
		strconv.FormatInt(timestamp, 10),
		nonce,
		instanceID,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

func NewNonce() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func SignedHeaders(key ed25519.PrivateKey, instanceID, method, path string, body []byte, now time.Time, nonce string) map[string]string {
	ts := now.Unix()
	sig := ed25519.Sign(key, []byte(CanonicalString(method, path, ts, nonce, instanceID, body)))
	headers := map[string]string{
		headerTimestamp: strconv.FormatInt(ts, 10),
		headerNonce:     nonce,
		headerSignature: base64.StdEncoding.EncodeToString(sig),
	}
	if instanceID != "" {
		headers[headerInstance] = instanceID
	}
	return headers
}

func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}
