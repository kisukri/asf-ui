package panel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_\-\x{4e00}-\x{9fff}]{3,32}$`)

var dummyHash = hashPassword("timing-equalizer-not-a-real-password")

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	check(err)
	return b
}

func argonKey(pw, salt []byte, t, m uint32, p uint8, keyLen uint32) []byte {
	select {
	case hashSlots <- struct{}{}:
	case <-time.After(15 * time.Second):
		panic(newErr(503, "busy", "服务器繁忙，请稍后再试。"))
	}
	defer func() { <-hashSlots }()
	hashCalls.Add(1)
	n := hashActive.Add(1)
	for p := hashPeak.Load(); n > p && !hashPeak.CompareAndSwap(p, n); p = hashPeak.Load() {
	}
	defer hashActive.Add(-1)
	defer debug.FreeOSMemory()
	return argon2.IDKey(pw, salt, t, m, p, keyLen)
}

var hashCalls, hashActive, hashPeak atomic.Int64

func hashPassword(pw string) string {
	salt := randBytes(argonSaltLen)
	key := argonKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func verifyPassword(stored, pw string) (bool, bool) {
	exists := stored != ""
	if !exists {
		stored = dummyHash
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<21 || t > 64 {
		return false, false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false, false
	}
	got := argonKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	ok := subtle.ConstantTimeCompare(got, want) == 1 && exists
	rehash := ok && (m != argonMemory || t != argonTime || p != argonThreads || len(want) != argonKeyLen)
	return ok, rehash
}

func validateUsername(u string) string {
	u = strings.TrimSpace(u)
	if !usernameRe.MatchString(u) {
		fail(400, "invalid_username", "用户名需为 3–32 位，可用字母、数字、下划线、短横线或中文。")
	}
	return u
}

func validateNewPassword(pw, username string) {
	n := runeLen(pw)
	if n < 10 {
		fail(400, "weak_password", "密码至少需要 10 个字符。")
	}
	if n > 128 {
		fail(400, "weak_password", "密码不能超过 128 个字符。")
	}
	if strings.EqualFold(strings.TrimSpace(pw), strings.TrimSpace(username)) {
		fail(400, "weak_password", "密码不能与用户名相同。")
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < 4 {
		fail(400, "weak_password", "密码过于简单，请使用更多不同的字符。")
	}
}

func newToken(nbytes int) string { return base64.RawURLEncoding.EncodeToString(randBytes(nbytes)) }

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func constEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

const inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newInviteCode() string {
	groups := make([]string, 4)
	for g := range groups {
		var sb strings.Builder
		for i := 0; i < 5; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(inviteAlphabet))))
			check(err)
			sb.WriteByte(inviteAlphabet[n.Int64()])
		}
		groups[g] = sb.String()
	}
	return "ASFP-" + strings.Join(groups, "-")
}

var spaceRe = regexp.MustCompile(`\s+`)

func normalizeInviteCode(code string) string {
	return strings.ToUpper(spaceRe.ReplaceAllString(code, ""))
}

type CredentialBox struct {
	keys    map[string]cipher.AEAD
	primary string
}

var kidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

func newCredentialBox(spec string) (*CredentialBox, error) {
	b := &CredentialBox{keys: map[string]cipher.AEAD{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, ":")
		kid = strings.TrimSpace(kid)
		if !ok || !kidRe.MatchString(kid) {
			return nil, fmt.Errorf("CREDENTIAL_KEYS must look like kid:base64key")
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("key %s in CREDENTIAL_KEYS must be 32 bytes, base64 encoded", kid)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		b.keys[kid] = gcm
		if b.primary == "" {
			b.primary = kid
		}
	}
	if b.primary == "" {
		return nil, fmt.Errorf("CREDENTIAL_KEYS is not set. run `asf-panel gen-key` and put the result in the environment")
	}
	return b, nil
}

func (b *CredentialBox) Encrypt(plain, aad string) string {
	nonce := randBytes(12)
	ct := b.keys[b.primary].Seal(nil, nonce, []byte(plain), []byte(aad))
	return "v1:" + b.primary + ":" + base64.URLEncoding.EncodeToString(append(nonce, ct...))
}

func (b *CredentialBox) Decrypt(blob, aad string) string {
	bad := newErr(500, "credential_unreadable", "无法读取已保存的连接凭据，请重新编辑该连接。")
	parts := strings.SplitN(blob, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		panic(bad)
	}
	gcm, ok := b.keys[parts[1]]
	if !ok {
		panic(bad)
	}
	data, err := base64.URLEncoding.DecodeString(parts[2])
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[2])
	}
	if err != nil || len(data) < 12+16 {
		panic(bad)
	}
	plain, err := gcm.Open(nil, data[:12], data[12:], []byte(aad))
	if err != nil {
		panic(bad)
	}
	return string(plain)
}

func (b *CredentialBox) Fingerprint(value string) string {
	key := sha256.Sum256([]byte("asfp-fingerprint:" + b.primary))
	m := hmac.New(sha256.New, key[:])
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}

func genKey() string {
	return "k" + hex.EncodeToString(randBytes(3)) + ":" + base64.StdEncoding.EncodeToString(randBytes(32))
}
