// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package log

import "go.opentelemetry.io/otel/attribute"

// KeyValue is a key/value pair attached to a [Record].
//
// This local compatibility build of go.opentelemetry.io/otel/log keeps the
// pre-v1 spelling that go.temporal.io/server v1.32.2 still uses. The v1 module
// replaced it with [attribute.KeyValue].
type KeyValue = attribute.KeyValue

// String returns a key/value pair with a string value.
func String(key, value string) KeyValue { return attribute.String(key, value) }

// Int64 returns a key/value pair with an int64 value.
func Int64(key string, value int64) KeyValue { return attribute.Int64(key, value) }

// Bool returns a key/value pair with a bool value.
func Bool(key string, value bool) KeyValue { return attribute.Bool(key, value) }

// Float64 returns a key/value pair with a float64 value.
func Float64(key string, value float64) KeyValue { return attribute.Float64(key, value) }
