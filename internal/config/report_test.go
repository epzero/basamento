package config

import (
	"bytes"
	"encoding/base64"
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

func TestReportRedactsEveryCredentialForm(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cases := []struct {
		name    string
		key     string
		value   string
		want    string
		secrets []string
	}{
		{
			name:    "query password",
			key:     "sql.url",
			value:   "postgres://ana@db.example.test/kit?password=s3cr3t-query",
			want:    `sql.url: postgres://***@db.example.test/kit?password=*** (env)`,
			secrets: []string{"s3cr3t-query"},
		},
		{
			name:    "s3 query credentials",
			key:     "blob.url",
			value:   "s3://bucket?access_key_id=AKIAEXAMPLE&secret_access_key=SECRETEXAMPLE&session_token=TOKENEXAMPLE",
			want:    `blob.url: s3://bucket?access_key_id=***&secret_access_key=***&session_token=*** (env)`,
			secrets: []string{"SECRETEXAMPLE"},
		},
		{
			name:    "aws query credentials",
			key:     "blob.url",
			value:   "s3://bucket?aws_access_key_id=AKIAEXAMPLE&aws_secret_access_key=AWSSECRETEXAMPLE",
			want:    `blob.url: s3://bucket?aws_access_key_id=***&aws_secret_access_key=*** (env)`,
			secrets: []string{"AKIAEXAMPLE", "AWSSECRETEXAMPLE"},
		},
		{
			name:    "lone userinfo token",
			key:     "bus.url",
			value:   "nats://bus-token-value@bus.example.test",
			want:    `bus.url: nats://***@bus.example.test (env)`,
			secrets: []string{"bus-token-value"},
		},
		{
			name:    "localsecrets key",
			key:     "secrets.url",
			value:   "localsecrets://" + key,
			want:    `secrets.url: localsecrets://*** (env)`,
			secrets: []string{key},
		},
		{
			name:    "opaque url",
			key:     "bus.url",
			value:   "opaque:secret-token-value",
			want:    `bus.url: opaque://*** (env)`,
			secrets: []string{"secret-token-value"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(Options{Env: []string{EnvironName(tc.key) + "=" + tc.value}})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			got.Report(&buf)
			report := buf.String()
			if !strings.Contains(report, tc.want) {
				t.Fatalf("report does not contain %q:\n%s", tc.want, report)
			}
			for _, secret := range tc.secrets {
				if strings.Contains(report, secret) {
					t.Fatalf("report leaks %q:\n%s", secret, report)
				}
			}
		})
	}
}
