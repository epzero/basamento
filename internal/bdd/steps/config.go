package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/config"
)

func registerConfig(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Config = newConfigWorld()
		return ctx, nil
	})
	sc.Step(`^a configuration file sets "([^"]*)" to "([^"]*)"(?: and "([^"]*)" to "([^"]*)")?$`, func(ctx context.Context, key, value, key2, value2 string) error {
		w := world(ctx).Config
		w.file[key] = value
		if key2 != "" {
			w.file[key2] = value2
		}
		return nil
	})
	sc.Step(`^the environment sets "([^"]*)" to "([^"]*)"$`, func(ctx context.Context, name, value string) error {
		w := world(ctx).Config
		w.env = append(w.env, name+"="+value)
		return nil
	})
	sc.Step(`^a "kit serve" flag sets "([^"]*)" to "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		world(ctx).Config.flags[key] = value
		return nil
	})
	sc.Step(`^the configuration is loaded$`, func(ctx context.Context) error {
		return world(ctx).Config.load()
	})
	sc.Step(`^"([^"]*)" is "([^"]*)" from "([^"]*)"$`, func(ctx context.Context, key, want, source string) error {
		got, src, ok := world(ctx).Config.result.Setting(key)
		if !ok {
			return fmt.Errorf("%s has no value", key)
		}
		if got != want || string(src) != source {
			return fmt.Errorf("%s = %q from %q, want %q from %q", key, got, src, want, source)
		}
		return nil
	})
	sc.Step(`^the configuration is validated$`, func(ctx context.Context) error {
		return world(ctx).Config.validate()
	})
	sc.Step(`^validation succeeds$`, func(ctx context.Context) error {
		if errs := world(ctx).Config.errs; len(errs) != 0 {
			return fmt.Errorf("validation failed: %v", errs)
		}
		return nil
	})
	sc.Step(`^validation fails with (\d+) errors?$`, func(ctx context.Context, n int) error {
		return expectErrors(world(ctx).Config.errs, n)
	})
	sc.Step(`^validation fails with (\d+) errors? naming "([^"]*)"$`, func(ctx context.Context, n int, key string) error {
		if err := expectErrors(world(ctx).Config.errs, n); err != nil {
			return err
		}
		return expectErrorKeys(world(ctx).Config.errs, []string{key})
	})
	sc.Step(`^the errors name "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, a, b, c string) error {
		return expectErrorKeys(world(ctx).Config.errs, []string{a, b, c})
	})
	sc.Step(`^the error text names the scheme "([^"]*)"$`, func(ctx context.Context, scheme string) error {
		for _, err := range world(ctx).Config.errs {
			if strings.Contains(err.Error(), `"`+scheme+`"`) {
				return nil
			}
		}
		return fmt.Errorf("no error names scheme %q: %v", scheme, world(ctx).Config.errs)
	})
	sc.Step(`^the error text does not contain ((?:any of )?"[^"]*"(?:, "[^"]*")+(?: or "[^"]*")?)$`, func(ctx context.Context, list string) error {
		errs := world(ctx).Config.errs
		if len(errs) == 0 {
			return fmt.Errorf("there is no error to inspect")
		}
		for _, token := range quotedTokens(list) {
			for _, err := range errs {
				if strings.Contains(err.Error(), token) {
					return fmt.Errorf("error %q contains %q", err.Error(), token)
				}
			}
		}
		return nil
	})
	sc.Step(`^the error text may name the scheme "([^"]*)" and does not contain "([^"]*)"$`, func(ctx context.Context, _, secret string) error {
		for _, err := range world(ctx).Config.errs {
			if strings.Contains(err.Error(), secret) {
				return fmt.Errorf("error %q contains %q", err.Error(), secret)
			}
		}
		return nil
	})
	sc.Step(`^the configuration fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		errs := world(ctx).Config.errs
		if len(errs) != 1 {
			return fmt.Errorf("got %d errors, want 1: %v", len(errs), errs)
		}
		return expectErrCode(errs[0], code)
	})
	sc.Step(`^the configuration fails with code "([^"]*)" naming "([^"]*)"$`, func(ctx context.Context, code, key string) error {
		errs := world(ctx).Config.errs
		if len(errs) != 1 {
			return fmt.Errorf("got %d errors, want 1: %v", len(errs), errs)
		}
		if err := expectCode(errs[0], code); err != nil {
			return err
		}
		return expectErrorKeys(errs, []string{key})
	})
	sc.Step(`^the effective configuration is reported$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if err := w.validate(); err != nil {
			return err
		}
		var buf bytes.Buffer
		w.result.Report(&buf)
		w.report = buf.String()
		return nil
	})
	sc.Step(`^"([^"]*)" is printed as "([^"]*)" from "([^"]*)"$`, func(ctx context.Context, key, want, source string) error {
		for _, line := range strings.Split(world(ctx).Config.report, "\n") {
			value, src, ok := reportLine(line)
			if ok && valueKey(line) == key {
				if value != want || src != source {
					return fmt.Errorf("%s printed as %q from %q, want %q from %q", key, value, src, want, source)
				}
				return nil
			}
		}
		return fmt.Errorf("report has no line for %s:\n%s", key, world(ctx).Config.report)
	})
	sc.Step(`^the report contains no "([^"]*)"$`, func(ctx context.Context, secret string) error {
		if strings.Contains(world(ctx).Config.report, secret) {
			return fmt.Errorf("report contains %q:\n%s", secret, world(ctx).Config.report)
		}
		return nil
	})

	sc.Step(`^the embedded profile with "([^"]*)" set to "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Config
		w.file["profile"] = config.ProfileEmbedded
		w.file[key] = value
		return nil
	})
	sc.Step(`^the cloud profile with "([^"]*)" set to "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Config
		w.file["profile"] = config.ProfileCloud
		w.file[key] = value
		for k, v := range cloudPortURLs {
			if k != key {
				w.file[k] = v
			}
		}
		return nil
	})
	sc.Step(`^the cloud profile omits "([^"]*)"$`, func(ctx context.Context, omit string) error {
		w := world(ctx).Config
		w.file["profile"] = config.ProfileCloud
		for k, v := range cloudPortURLs {
			if k != omit {
				w.file[k] = v
			}
		}
		return nil
	})
	sc.Step(`^the embedded profile with "([^"]*)" as "profile"$`, func(ctx context.Context, value string) error {
		world(ctx).Config.file["profile"] = value
		return nil
	})
	sc.Step(`^the embedded profile with the listener "([^"]*)"$`, func(ctx context.Context, addr string) error {
		w := world(ctx).Config
		w.file["profile"] = config.ProfileEmbedded
		w.file["listen.addr"] = addr
		w.listen = addr
		return nil
	})
	sc.Step(`^"([^"]*)" is selected as "([^"]*)"$`, func(ctx context.Context, key, want string) error {
		w := world(ctx).Config
		if err := w.validate(); err != nil {
			return err
		}
		if len(w.errs) != 0 {
			return fmt.Errorf("validation failed: %v", w.errs)
		}
		got := w.result.Config.Implementation(strings.TrimSuffix(key, ".url"))
		if got != want {
			return fmt.Errorf("%s selected %q, want %q", key, got, want)
		}
		return nil
	})
}

var cloudPortURLs = map[string]string{
	"sql.url":     "postgres://db.example.test/kit",
	"blob.url":    "s3://kit-buckets/assets",
	"bus.url":     "nats://bus.example.test:4222",
	"secrets.url": "awskms:///arn:aws:kms:kit",
}

func expectErrors(errs []error, n int) error {
	if len(errs) != n {
		return fmt.Errorf("got %d errors, want %d: %v", len(errs), n, errs)
	}
	return nil
}

func expectErrorKeys(errs []error, keys []string) error {
	got := make([]string, 0, len(errs))
	for _, err := range errs {
		got = append(got, errorKeyOf(err))
	}
	if strings.Join(got, ",") != strings.Join(keys, ",") {
		return fmt.Errorf("errors name %v, want %v", got, keys)
	}
	return nil
}

func errorKeyOf(err error) string {
	var e *core.Error
	if ok := asCoreError(err, &e); !ok {
		return ""
	}
	key, _ := e.Details["key"].(string)
	return key
}

func asCoreError(err error, target **core.Error) bool {
	return errors.As(err, target)
}

func expectErrCode(err error, code string) error {
	if got := string(core.CodeOf(err)); got != code {
		return fmt.Errorf("code = %s, want %s", got, code)
	}
	return nil
}

func quotedTokens(list string) []string {
	re := regexp.MustCompile(`"([^"]*)"`)
	var out []string
	for _, match := range re.FindAllStringSubmatch(list, -1) {
		out = append(out, match[1])
	}
	return out
}

// valueKey and reportLine parse a Report line `key: value (source)`.
func valueKey(line string) string {
	key, _, ok := strings.Cut(line, ": ")
	if !ok {
		return ""
	}
	return key
}

func reportLine(line string) (value, source string, ok bool) {
	key, rest, ok := strings.Cut(line, ": ")
	if !ok || key == "" {
		return "", "", false
	}
	open := strings.LastIndex(rest, " (")
	if open < 0 || !strings.HasSuffix(rest, ")") {
		return "", "", false
	}
	return rest[:open], rest[open+2 : len(rest)-1], true
}
