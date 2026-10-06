package integration

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// TestEncryptPushRoundTrip decrypts a Web Push message the way a browser
// does (RFC 8291 §3.4) and finds the payload.
func TestEncryptPushRoundTrip(t *testing.T) {
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	enc := base64.RawURLEncoding
	target := PushTarget{Endpoint: "https://push.example/abc", P256DH: enc.EncodeToString(ua.PublicKey().Bytes()), Auth: enc.EncodeToString(auth)}
	payload := []byte(`{"title":"Jadwal terbit","body":"Shift Anda minggu depan"}`)
	body, err := EncryptPush(target, payload, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header: rs %d idlen %d", rs, idlen)
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(asPub)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub.Bytes()), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatal("decrypt:", err)
	}
	if string(plain[:len(plain)-1]) != string(payload) || plain[len(plain)-1] != 0x02 {
		t.Fatalf("plaintext %q", plain)
	}
	if _, err := EncryptPush(PushTarget{P256DH: "bad", Auth: target.Auth}, payload, rand.Reader); err == nil {
		t.Fatal("an invalid browser key must be refused")
	}
}

func TestVAPIDToken(t *testing.T) {
	k := devVAPIDKey("test-seed")
	if !devVAPIDKey("test-seed").Equal(k) {
		t.Fatal("the sandbox key must be stable for a seed")
	}
	tok, err := VAPIDToken(k, "https://fcm.googleapis.com", "mailto:it@club.test", time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	enc := base64.RawURLEncoding
	claims, _ := enc.DecodeString(parts[1])
	var c map[string]any
	if err := json.Unmarshal(claims, &c); err != nil || c["aud"] != "https://fcm.googleapis.com" || c["sub"] != "mailto:it@club.test" {
		t.Fatalf("claims %s", claims)
	}
	sig, _ := enc.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&k.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("ES256 signature does not verify")
	}
	raw, _ := k.Bytes()
	parsed, err := ParseVAPIDKey(enc.EncodeToString(raw))
	if err != nil || !parsed.Equal(k) {
		t.Fatalf("parse: %v", err)
	}
	if len(publicKeyB64(k)) != 87 { // 65 bytes base64url
		t.Fatalf("public key %q", publicKeyB64(k))
	}
}
