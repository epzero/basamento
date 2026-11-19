package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestReportMasksPasswords(t *testing.T) {
	path := writeConfig(t, `profile: cloud
sql:
  url: "postgres://ana:s3cr3t-value@db.example.test/kit"
blob:
  url: "s3://kit-buckets/assets"
bus:
  url: "nats://bus.example.test:4222"
secrets:
  url: "awskms:///arn:aws:kms:kit"
`)
	got, err := Load(Options{File: path})
	if err != nil {
		t.Fatal(err)
	}
	if errs := got.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	var buf bytes.Buffer
	got.Report(&buf)
	report := buf.String()
	if !strings.Contains(report, `sql.url: postgres://ana:***@db.example.test/kit (file)`) {
		t.Fatalf("report does not mask sql.url:\n%s", report)
	}
	if !strings.Contains(report, `log.level: info (default)`) {
		t.Fatalf("report does not show the default source:\n%s", report)
	}
	if strings.Contains(report, "s3cr3t-value") {
		t.Fatalf("report leaks the password:\n%s", report)
	}
}

func TestReportMasksPercentEncodedPassword(t *testing.T) {
	path := writeConfig(t, `profile: cloud
sql:
  url: "postgres://ana:p%40ss%3Aword@db.example.test/kit"
blob:
  url: "s3://kit-buckets/assets"
bus:
  url: "nats://bus.example.test:4222"
secrets:
  url: "awskms:///arn:aws:kms:kit"
`)
	got, err := Load(Options{File: path})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	got.Report(&buf)
	if strings.Contains(buf.String(), "p%40ss") || strings.Contains(buf.String(), "p@ss") {
		t.Fatalf("report leaks the password:\n%s", buf.String())
	}
}
