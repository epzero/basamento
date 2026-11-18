package blob

import (
	"testing"
	"time"
)

func TestSignUploadDistinctFromRead(t *testing.T) {
	t.Parallel()
	key := []byte("signing-key-for-tests-only")
	tenant := "acme"
	hex := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	stage := "stage_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	exp := time.Date(2026, 11, 16, 9, 15, 0, 0, time.UTC).Unix()
	read := signRead(key, tenant, hex, exp)
	up := signUpload(key, tenant, stage, exp, 1024, "text/plain")
	if read == up {
		t.Fatal("read and upload signatures must differ")
	}
	now := time.Unix(exp-1, 0)
	if !verifyUpload(key, tenant, stage, up, exp, 1024, "text/plain", now) {
		t.Fatal("upload signature should verify")
	}
	if verifyRead(key, tenant, hex, up, exp, now) {
		t.Fatal("upload sig must not verify as read sig")
	}
	if verifyUpload(key, tenant, stage+"x", up, exp, 1024, "text/plain", now) {
		t.Fatal("wrong staging key must not verify")
	}
}

func TestVerifyUploadChecksSignatureBeforeExpiry(t *testing.T) {
	t.Parallel()
	key := []byte("signing-key-for-tests-only")
	tenant := "acme"
	stage := "stage_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	exp := int64(1000)
	readSig := signRead(key, tenant, "deadbeef", exp)
	if verifyUpload(key, tenant, stage, readSig, exp, 100, "text/plain", time.Unix(exp+1, 0)) {
		t.Fatal("wrong signature must not verify even when expired")
	}
	upSig := signUpload(key, tenant, stage, exp, 100, "text/plain")
	if verifyUpload(key, tenant, stage, upSig, exp, 100, "text/plain", time.Unix(exp, 0)) {
		t.Fatal("good signature must reject at expiry")
	}
	if !verifyUpload(key, tenant, stage, upSig, exp, 100, "text/plain", time.Unix(exp-1, 0)) {
		t.Fatal("good signature should verify before expiry")
	}
}
