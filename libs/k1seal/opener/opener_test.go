package opener_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
)

// Демо-ключи для тестов, не для эксплуатации.
func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

const (
	plainCase = "ИБ-2026-000417"
	requestID = "0192f0c1-7a2b-7c3d-8e4f-5a6b7c8d9e0f"
	otherID   = "0192f0c1-7a2b-7c3d-8e4f-5a6b7c8d9e10"
)

// setup возвращает связку дважды: kr шифрует, op расшифровывает (в сервисах это одна и та же связка).
func setup(t *testing.T, spec string) (*opener.Keyring, *opener.Keyring) {
	t.Helper()
	kr, err := opener.ParseKeyring(spec)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	return kr, kr
}

func TestRoundTrip(t *testing.T) {
	kr, op := setup(t, "cur:"+key(1))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := op.Open(s, requestID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plainCase {
		t.Fatalf("Open вернул %q, ожидался %q", got, plainCase)
	}
}

// Номер нормализуется (обрезка пробелов) до шифрования.
func TestSealNormalizes(t *testing.T) {
	kr, op := setup(t, "cur:"+key(1))
	s, err := kr.Seal("  \t"+plainCase+" \n", requestID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := op.Open(s, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if got != plainCase {
		t.Fatalf("ожидался обрезанный номер %q, получено %q", plainCase, got)
	}
}

// Подмена любого байта шифротекста обнаруживается.
func TestTamperedCiphertextFails(t *testing.T) {
	kr, op := setup(t, "cur:"+key(1))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	header := "v1.cur."
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(s), header))
	if err != nil {
		t.Fatalf("разбор конверта: %v", err)
	}
	for i := range raw {
		mut := bytes.Clone(raw)
		mut[i] ^= 0x01
		forged := k1seal.Sealed(header + base64.RawURLEncoding.EncodeToString(mut))
		got, err := op.Open(forged, requestID)
		if !errors.Is(err, k1seal.ErrOpen) {
			t.Fatalf("байт %d: ожидалась ErrOpen, получено %v (%q)", i, err, got)
		}
		if got != "" {
			t.Fatalf("байт %d: при ошибке вернулся текст %q", i, got)
		}
	}
	// Укороченный шифротекст (без последнего байта тега).
	short := k1seal.Sealed(header + base64.RawURLEncoding.EncodeToString(raw[:len(raw)-1]))
	if _, err := op.Open(short, requestID); !errors.Is(err, k1seal.ErrOpen) {
		t.Fatalf("укороченный: ожидалась ErrOpen, получено %v", err)
	}
}

// Конверт чужой заявки не открывается (AAD = request_id).
func TestForeignRequestIDFails(t *testing.T) {
	kr, op := setup(t, "cur:"+key(1))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{otherID, "", strings.ToUpper(requestID), requestID + " "} {
		if _, err := op.Open(s, id); err == nil {
			t.Fatalf("конверт открылся с чужим request_id %q", id)
		} else if strings.Contains(err.Error(), plainCase) {
			t.Fatalf("ошибка содержит открытый текст: %v", err)
		}
	}
}

func TestUnknownKeyIDFails(t *testing.T) {
	krA, _ := setup(t, "a:"+key(1))
	_, opB := setup(t, "b:"+key(2))
	s, err := krA.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opB.Open(s, requestID); !errors.Is(err, k1seal.ErrUnknownKey) {
		t.Fatalf("ожидалась ErrUnknownKey, получено %v", err)
	}
}

// Замена kid в заголовке на другой известный ключ не проходит: заголовок входит в AAD.
func TestSwappedKeyIDFails(t *testing.T) {
	kr, op := setup(t, "a:"+key(1)+",b:"+key(2))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	forged := k1seal.Sealed("v1.b." + strings.TrimPrefix(string(s), "v1.a."))
	if _, err := op.Open(forged, requestID); !errors.Is(err, k1seal.ErrOpen) {
		t.Fatalf("ожидалась ErrOpen, получено %v", err)
	}
}

// Ротация: новый ключ шифрует, старый продолжает расшифровывать свои конверты.
func TestRotation(t *testing.T) {
	krOld, opOld := setup(t, "old:"+key(1))
	sOld, err := krOld.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}

	krNew, opNew := setup(t, "new:"+key(2)+",old:"+key(1))
	got, err := opNew.Open(sOld, requestID)
	if err != nil || got != plainCase {
		t.Fatalf("старый конверт после ротации: %q, %v", got, err)
	}
	sNew, err := krNew.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if sNew.KeyID() != "new" {
		t.Fatalf("после ротации шифрует %q, ожидался new", sNew.KeyID())
	}
	if got, err := opNew.Open(sNew, requestID); err != nil || got != plainCase {
		t.Fatalf("новый конверт: %q, %v", got, err)
	}
	if _, err := opOld.Open(sNew, requestID); !errors.Is(err, k1seal.ErrUnknownKey) {
		t.Fatalf("связка без нового ключа: ожидалась ErrUnknownKey, получено %v", err)
	}
}

func TestMalformedEnvelopeFails(t *testing.T) {
	_, op := setup(t, "cur:"+key(1))
	for _, s := range []k1seal.Sealed{"", "garbage", k1seal.Sealed(plainCase), "v2.cur.AAAA", "v1.cur.@@@"} {
		_, err := op.Open(s, requestID)
		if !errors.Is(err, k1seal.ErrMalformed) {
			t.Fatalf("%q: ожидалась ErrMalformed, получено %v", string(s), err)
		}
		if strings.Contains(err.Error(), plainCase) {
			t.Fatalf("ошибка содержит открытый текст: %v", err)
		}
	}
}

func TestNilKeyringFails(t *testing.T) {
	var kr *opener.Keyring
	if _, err := kr.Open(k1seal.Sealed("v1.cur.AAAA"), requestID); !errors.Is(err, k1seal.ErrKeyring) {
		t.Fatalf("Open: ожидалась ErrKeyring, получено %v", err)
	}
	if _, err := kr.Seal(plainCase, requestID); !errors.Is(err, k1seal.ErrKeyring) {
		t.Fatalf("Seal: ожидалась ErrKeyring, получено %v", err)
	}
}
