package tron

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"payment-gateway/internal/config"

	"github.com/ethereum/go-ethereum/crypto"
)

const (
	xpubVersion uint32 = 0x0488b21e
	xprvVersion uint32 = 0x0488ade4
	tronPrefix  byte   = 0x41
)

var base58Alphabet = []byte("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz")

type Client struct {
	cfg *config.Config
}

type ExtendedKeyPair struct {
	SeedHex string
	XPrv    string
	XPub    string
	Samples []string
}

type extendedPublicKey struct {
	Depth       byte
	ParentFP    [4]byte
	ChildNumber uint32
	ChainCode   []byte
	PublicKey   []byte
}

type extendedPrivateKey struct {
	Depth       byte
	ParentFP    [4]byte
	ChildNumber uint32
	ChainCode   []byte
	PrivateKey  []byte
}

func NewClient(cfg *config.Config) *Client {
	return &Client{cfg: cfg}
}

func (c *Client) DeriveAddress(index int) (string, error) {
	if c.cfg.TronXPub == "" {
		return "", errors.New("TRON_XPUB nÃ£o configurado")
	}
	return DeriveAddress(c.cfg.TronXPub, index)
}

func DeriveAddress(xpub string, index int) (string, error) {
	if index < 0 {
		return "", fmt.Errorf("Ã­ndice de derivaÃ§Ã£o invÃ¡lido")
	}
	key, err := parseXPub(xpub)
	if err != nil {
		return "", err
	}
	child, err := derivePublicChild(key, uint32(index))
	if err != nil {
		return "", err
	}
	pub, err := crypto.DecompressPubkey(child.PublicKey)
	if err != nil {
		return "", err
	}
	raw := crypto.Keccak256(crypto.FromECDSAPub(pub)[1:])[12:]
	payload := append([]byte{tronPrefix}, raw...)
	return base58CheckEncode(payload), nil
}

func IsAddress(addr string) bool {
	raw, err := base58CheckDecode(strings.TrimSpace(addr))
	return err == nil && len(raw) == 21 && raw[0] == tronPrefix
}

func GenerateAccountKeys(sampleCount int) (*ExtendedKeyPair, error) {
	if sampleCount < 0 {
		sampleCount = 0
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	master, err := masterKey(seed)
	if err != nil {
		return nil, err
	}
	account := master
	for _, child := range []uint32{hardened(44), hardened(195), hardened(0)} {
		account, err = derivePrivateChild(account, child)
		if err != nil {
			return nil, err
		}
	}
	xpub, err := serializeXPub(account)
	if err != nil {
		return nil, err
	}
	xprv := serializeXPrv(account)
	out := &ExtendedKeyPair{SeedHex: hex.EncodeToString(seed), XPrv: xprv, XPub: xpub}
	for i := 0; i < sampleCount; i++ {
		addr, err := DeriveAddress(xpub, i)
		if err != nil {
			return nil, err
		}
		out.Samples = append(out.Samples, addr)
	}
	return out, nil
}

func parseXPub(value string) (*extendedPublicKey, error) {
	raw, err := base58CheckDecode(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("TRON_XPUB invÃ¡lido: %w", err)
	}
	if len(raw) != 78 {
		return nil, fmt.Errorf("TRON_XPUB invÃ¡lido: tamanho %d", len(raw))
	}
	if binary.BigEndian.Uint32(raw[0:4]) != xpubVersion {
		return nil, errors.New("TRON_XPUB precisa ser uma chave xpub")
	}
	var fp [4]byte
	copy(fp[:], raw[5:9])
	return &extendedPublicKey{
		Depth:       raw[4],
		ParentFP:    fp,
		ChildNumber: binary.BigEndian.Uint32(raw[9:13]),
		ChainCode:   append([]byte(nil), raw[13:45]...),
		PublicKey:   append([]byte(nil), raw[45:78]...),
	}, nil
}

func derivePublicChild(parent *extendedPublicKey, index uint32) (*extendedPublicKey, error) {
	if index >= 0x80000000 {
		return nil, errors.New("nÃ£o Ã© possÃ­vel derivar filho hardened a partir de xpub")
	}
	mac := hmac.New(sha512.New, parent.ChainCode)
	mac.Write(parent.PublicKey)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index)
	mac.Write(idx[:])
	sum := mac.Sum(nil)
	il, ir := sum[:32], sum[32:]
	curve := crypto.S256()
	ilInt := new(big.Int).SetBytes(il)
	if ilInt.Sign() == 0 || ilInt.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("derivaÃ§Ã£o BIP32 invÃ¡lida")
	}
	parentPub, err := crypto.DecompressPubkey(parent.PublicKey)
	if err != nil {
		return nil, err
	}
	x1, y1 := curve.ScalarBaseMult(il)
	x2, y2 := curve.Add(x1, y1, parentPub.X, parentPub.Y)
	if x2 == nil || y2 == nil {
		return nil, errors.New("ponto pÃºblico invÃ¡lido")
	}
	childPub := crypto.CompressPubkey(&ecdsa.PublicKey{Curve: curve, X: x2, Y: y2})
	fp := fingerprint(parent.PublicKey)
	return &extendedPublicKey{
		Depth:       parent.Depth + 1,
		ParentFP:    fp,
		ChildNumber: index,
		ChainCode:   append([]byte(nil), ir...),
		PublicKey:   childPub,
	}, nil
}

func masterKey(seed []byte) (*extendedPrivateKey, error) {
	mac := hmac.New(sha512.New, []byte("Bitcoin seed"))
	mac.Write(seed)
	sum := mac.Sum(nil)
	if new(big.Int).SetBytes(sum[:32]).Cmp(crypto.S256().Params().N) >= 0 {
		return nil, errors.New("seed gerou chave mestre invÃ¡lida")
	}
	return &extendedPrivateKey{ChainCode: append([]byte(nil), sum[32:]...), PrivateKey: append([]byte(nil), sum[:32]...)}, nil
}

func derivePrivateChild(parent *extendedPrivateKey, index uint32) (*extendedPrivateKey, error) {
	mac := hmac.New(sha512.New, parent.ChainCode)
	if index >= 0x80000000 {
		mac.Write([]byte{0})
		mac.Write(parent.PrivateKey)
	} else {
		priv, err := crypto.ToECDSA(parent.PrivateKey)
		if err != nil {
			return nil, err
		}
		mac.Write(crypto.CompressPubkey(&priv.PublicKey))
	}
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index)
	mac.Write(idx[:])
	sum := mac.Sum(nil)
	il := new(big.Int).SetBytes(sum[:32])
	n := crypto.S256().Params().N
	if il.Sign() == 0 || il.Cmp(n) >= 0 {
		return nil, errors.New("derivaÃ§Ã£o BIP32 invÃ¡lida")
	}
	key := new(big.Int).SetBytes(parent.PrivateKey)
	key.Add(key, il)
	key.Mod(key, n)
	if key.Sign() == 0 {
		return nil, errors.New("chave filha invÃ¡lida")
	}
	fp, err := privateFingerprint(parent.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &extendedPrivateKey{
		Depth:       parent.Depth + 1,
		ParentFP:    fp,
		ChildNumber: index,
		ChainCode:   append([]byte(nil), sum[32:]...),
		PrivateKey:  padded32(key),
	}, nil
}

func serializeXPub(priv *extendedPrivateKey) (string, error) {
	key, err := crypto.ToECDSA(priv.PrivateKey)
	if err != nil {
		return "", err
	}
	raw := make([]byte, 78)
	binary.BigEndian.PutUint32(raw[0:4], xpubVersion)
	raw[4] = priv.Depth
	copy(raw[5:9], priv.ParentFP[:])
	binary.BigEndian.PutUint32(raw[9:13], priv.ChildNumber)
	copy(raw[13:45], priv.ChainCode)
	copy(raw[45:78], crypto.CompressPubkey(&key.PublicKey))
	return base58CheckEncode(raw), nil
}

func serializeXPrv(priv *extendedPrivateKey) string {
	raw := make([]byte, 78)
	binary.BigEndian.PutUint32(raw[0:4], xprvVersion)
	raw[4] = priv.Depth
	copy(raw[5:9], priv.ParentFP[:])
	binary.BigEndian.PutUint32(raw[9:13], priv.ChildNumber)
	copy(raw[13:45], priv.ChainCode)
	raw[45] = 0
	copy(raw[46:78], priv.PrivateKey)
	return base58CheckEncode(raw)
}

func hardened(index uint32) uint32 {
	return index + 0x80000000
}

func privateFingerprint(privBytes []byte) ([4]byte, error) {
	priv, err := crypto.ToECDSA(privBytes)
	if err != nil {
		return [4]byte{}, err
	}
	return fingerprint(crypto.CompressPubkey(&priv.PublicKey)), nil
}

func fingerprint(pub []byte) [4]byte {
	sum := sha256.Sum256(pub)
	var out [4]byte
	copy(out[:], sum[:4])
	return out
}

func padded32(v *big.Int) []byte {
	out := make([]byte, 32)
	raw := v.Bytes()
	copy(out[32-len(raw):], raw)
	return out
}

func base58CheckEncode(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return base58Encode(append(append([]byte(nil), payload...), second[:4]...))
}

func base58CheckDecode(value string) ([]byte, error) {
	raw, err := base58Decode(value)
	if err != nil {
		return nil, err
	}
	if len(raw) < 5 {
		return nil, errors.New("payload curto")
	}
	payload, checksum := raw[:len(raw)-4], raw[len(raw)-4:]
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(checksum, second[:4]) {
		return nil, errors.New("checksum invÃ¡lido")
	}
	return payload, nil
}

func base58Encode(input []byte) string {
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range input {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(value string) ([]byte, error) {
	result := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range []byte(value) {
		idx := bytes.IndexByte(base58Alphabet, r)
		if idx < 0 {
			return nil, fmt.Errorf("caractere base58 invÃ¡lido: %q", r)
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(int64(idx)))
	}
	out := result.Bytes()
	for i := 0; i < len(value) && value[i] == base58Alphabet[0]; i++ {
		out = append([]byte{0}, out...)
	}
	return out, nil
}
