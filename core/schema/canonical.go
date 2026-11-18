package schema

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonicalize returns the RFC 8785 canonical form of text.
func Canonicalize(text []byte) ([]byte, error) {
	p := &canonParser{data: text, stack: nil}
	out, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.i < len(p.data) {
		return nil, invalidArg("trailing JSON", map[string]any{"location": ""})
	}
	return out, nil
}

type canonParser struct {
	data  []byte
	i     int
	stack []string
}

func (p *canonParser) loc() string {
	return ptrJoin(p.stack)
}

func (p *canonParser) parseValue() ([]byte, error) {
	p.skipSpace()
	if p.i >= len(p.data) {
		return nil, invalidArg("unexpected end of JSON", map[string]any{"location": p.loc()})
	}
	switch p.data[p.i] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parseString()
	case 'n':
		return p.parseLiteral("null", "null")
	case 't':
		return p.parseLiteral("true", "true")
	case 'f':
		return p.parseLiteral("false", "false")
	default:
		return p.parseNumber()
	}
}

func (p *canonParser) parseLiteral(word, out string) ([]byte, error) {
	if p.i+len(word) > len(p.data) || string(p.data[p.i:p.i+len(word)]) != word {
		return nil, invalidArgf("invalid literal at %s", p.loc())
	}
	p.i += len(word)
	return []byte(out), nil
}

type member struct {
	key string
	val []byte
}

func (p *canonParser) parseObject() ([]byte, error) {
	if p.data[p.i] != '{' {
		return nil, invalidArgf("expected object at %s", p.loc())
	}
	p.i++
	p.skipSpace()
	if p.i < len(p.data) && p.data[p.i] == '}' {
		p.i++
		return []byte("{}"), nil
	}
	seen := map[string]int{}
	var members []member
	for {
		p.skipSpace()
		if p.data[p.i] != '"' {
			return nil, invalidArgf("expected string key at %s", p.loc())
		}
		key, err := p.readStringKey()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[key]; dup {
			p.stack = append(p.stack, key)
			loc := p.loc()
			p.stack = p.stack[:len(p.stack)-1]
			return nil, invalidArg("duplicate object member", map[string]any{"location": loc})
		}
		seen[key] = 1
		p.skipSpace()
		if p.i >= len(p.data) || p.data[p.i] != ':' {
			return nil, invalidArgf("expected colon at %s", p.loc())
		}
		p.i++
		p.stack = append(p.stack, key)
		val, err := p.parseValue()
		p.stack = p.stack[:len(p.stack)-1]
		if err != nil {
			return nil, err
		}
		members = append(members, member{key: key, val: val})
		p.skipSpace()
		if p.i >= len(p.data) {
			return nil, invalidArgf("unterminated object at %s", p.loc())
		}
		if p.data[p.i] == '}' {
			p.i++
			break
		}
		if p.data[p.i] != ',' {
			return nil, invalidArgf("expected comma at %s", p.loc())
		}
		p.i++
	}
	sort.Slice(members, func(i, j int) bool {
		return utf16Less(members[i].key, members[j].key)
	})
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(canonicalString(m.key))
		buf.WriteByte(':')
		buf.Write(m.val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (p *canonParser) parseArray() ([]byte, error) {
	if p.data[p.i] != '[' {
		return nil, invalidArgf("expected array at %s", p.loc())
	}
	p.i++
	p.skipSpace()
	if p.i < len(p.data) && p.data[p.i] == ']' {
		p.i++
		return []byte("[]"), nil
	}
	var elems [][]byte
	idx := 0
	for {
		p.stack = append(p.stack, strconv.Itoa(idx))
		val, err := p.parseValue()
		p.stack = p.stack[:len(p.stack)-1]
		if err != nil {
			return nil, err
		}
		elems = append(elems, val)
		idx++
		p.skipSpace()
		if p.i >= len(p.data) {
			return nil, invalidArgf("unterminated array at %s", p.loc())
		}
		if p.data[p.i] == ']' {
			p.i++
			break
		}
		if p.data[p.i] != ',' {
			return nil, invalidArgf("expected comma at %s", p.loc())
		}
		p.i++
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, e := range elems {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(e)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

func (p *canonParser) parseString() ([]byte, error) {
	s, err := p.readStringKey()
	if err != nil {
		return nil, err
	}
	return canonicalString(s), nil
}

func (p *canonParser) readStringKey() (string, error) {
	if p.data[p.i] != '"' {
		return "", invalidArgf("expected string at %s", p.loc())
	}
	p.i++
	start := p.i
	var runes []rune
	for p.i < len(p.data) {
		c := p.data[p.i]
		if c == '"' {
			raw := string(p.data[start:p.i])
			if err := p.decodeStringContent(raw, &runes); err != nil {
				return "", err
			}
			p.i++
			return string(runes), nil
		}
		if c == '\\' {
			p.i++
			if p.i >= len(p.data) {
				return "", invalidArgf("truncated escape at %s", p.loc())
			}
		}
		p.i++
	}
	return "", invalidArgf("unterminated string at %s", p.loc())
}

func (p *canonParser) decodeStringContent(raw string, out *[]rune) error {
	i := 0
	for i < len(raw) {
		if raw[i] != '\\' {
			r, size := utf8.DecodeRuneInString(raw[i:])
			if r == utf8.RuneError && size == 1 {
				return invalidArg("invalid UTF-8", map[string]any{"location": p.loc()})
			}
			*out = append(*out, r)
			i += size
			continue
		}
		i++
		if i >= len(raw) {
			return invalidArgf("bad escape at %s", p.loc())
		}
		switch raw[i] {
		case '"', '\\', '/':
			*out = append(*out, rune(raw[i]))
			i++
		case 'b':
			*out = append(*out, '\b')
			i++
		case 'f':
			*out = append(*out, '\f')
			i++
		case 'n':
			*out = append(*out, '\n')
			i++
		case 'r':
			*out = append(*out, '\r')
			i++
		case 't':
			*out = append(*out, '\t')
			i++
		case 'u':
			if i+4 >= len(raw) {
				return invalidArgf("bad \\u escape at %s", p.loc())
			}
			var cp rune
			for j := 0; j < 4; j++ {
				h := unhex(raw[i+1+j])
				if h < 0 {
					return invalidArgf("bad \\u escape at %s", p.loc())
				}
				cp = cp<<4 | rune(h)
			}
			i += 5
			if utf16.IsSurrogate(cp) {
				if i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
					var low rune
					for j := 0; j < 4; j++ {
						h := unhex(raw[i+2+j])
						if h < 0 {
							return invalidArg("unpaired surrogate", map[string]any{"location": p.loc()})
						}
						low = low<<4 | rune(h)
					}
					if utf16.IsSurrogate(low) && utf16.IsSurrogate(cp) {
						combined := utf16.DecodeRune(cp, low)
						if combined != utf8.RuneError {
							*out = append(*out, combined)
							i += 6
							continue
						}
					}
				}
				return invalidArg("unpaired surrogate", map[string]any{"location": p.loc()})
			}
			*out = append(*out, cp)
		default:
			return invalidArgf("bad escape at %s", p.loc())
		}
	}
	if hasUnpairedSurrogate(string(*out)) {
		return invalidArg("unpaired surrogate", map[string]any{"location": p.loc()})
	}
	return nil
}

func unhex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b - 'a' + 10)
	case b >= 'A' && b <= 'F':
		return int(b - 'A' + 10)
	default:
		return -1
	}
}

func canonicalString(s string) []byte {
	var buf []byte
	buf = append(buf, '"')
	for _, r := range s {
		switch r {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\b':
			buf = append(buf, '\\', 'b')
		case '\f':
			buf = append(buf, '\\', 'f')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		default:
			if r < 0x20 {
				buf = append(buf, '\\', 'u', hexDigit(byte(r>>12)), hexDigit(byte((r>>8)&0xf)), hexDigit(byte((r>>4)&0xf)), hexDigit(byte(r&0xf)))
			} else {
				var tmp [utf8.UTFMax]byte
				n := utf8.EncodeRune(tmp[:], r)
				buf = append(buf, tmp[:n]...)
			}
		}
	}
	buf = append(buf, '"')
	return buf
}

func hexDigit(b byte) byte {
	const hexdigits = "0123456789abcdef"
	return hexdigits[b&0xf]
}

func (p *canonParser) parseNumber() ([]byte, error) {
	start := p.i
	for p.i < len(p.data) {
		c := p.data[p.i]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			p.i++
			continue
		}
		break
	}
	text := string(p.data[start:p.i])
	canonical, err := canonicalNumberText(text, p.loc())
	if err != nil {
		return nil, err
	}
	return []byte(canonical), nil
}

func canonicalNumberText(text, loc string) (string, error) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", invalidArg("invalid number", map[string]any{"location": loc})
	}
	if losesFloatPrecision(text, f) {
		return "", invalidArg("number is not an exact IEEE 754 double", map[string]any{"location": loc})
	}
	return ecmaNumber(f), nil
}

func losesFloatPrecision(text string, f float64) bool {
	text = strings.TrimSpace(text)
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return true
	}
	if text == ecmaNumber(f) {
		return false
	}
	if strings.ContainsAny(text, "eE") {
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return true
		}
		if f == 0 && !isZeroLiteral(text) {
			return true
		}
		return false
	}
	if strings.Contains(text, ".") {
		if text == ecmaNumber(f) {
			return false
		}
		rText, ok := new(big.Rat).SetString(text)
		if !ok {
			return true
		}
		rF := new(big.Rat).SetFloat64(f)
		if rText.Cmp(rF) != 0 {
			return true
		}
		tTrim := strings.TrimRight(strings.TrimRight(text, "0"), ".")
		eTrim := strings.TrimRight(strings.TrimRight(ecmaNumber(f), "0"), ".")
		return tTrim != eTrim
	}
	if isIntegerLiteral(text) {
		bi, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return true
		}
		if !bi.IsInt64() {
			return true
		}
		if bi.Int64() != int64(f) {
			return true
		}
		return false
	}
	rText, ok := new(big.Rat).SetString(text)
	if !ok {
		return true
	}
	rF := new(big.Rat).SetFloat64(f)
	return rText.Cmp(rF) != 0
}

func isIntegerLiteral(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if text[0] == '-' || text[0] == '+' {
		text = text[1:]
	}
	if text == "" {
		return false
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isZeroLiteral(text string) bool {
	switch strings.TrimSpace(text) {
	case "0", "-0", "+0", "0.0", "-0.0":
		return true
	default:
		return false
	}
}

// ecmaNumber formats f as ECMAScript would after RFC 8785 shortest form rules.
func ecmaNumber(f float64) string {
	if math.Signbit(f) && f == 0 {
		f = 0
	}
	if !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) && math.Abs(f) <= 9007199254740992 {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	abs := math.Abs(f)
	if f != 0 && abs < 1 && abs >= 0.000001 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		return s
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	// ECMAScript uses lowercase e and includes + in positive exponents with 2+ digits.
	if strings.ContainsAny(s, "eE") {
		s = strings.Replace(s, "E", "e", 1)
		parts := strings.SplitN(s, "e", 2)
		exp, _ := strconv.Atoi(parts[1])
		if exp > 0 {
			s = parts[0] + "e+" + strconv.Itoa(exp)
		} else {
			s = parts[0] + "e" + strconv.Itoa(exp)
		}
	}
	return s
}

func utf16Less(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	n := len(ua)
	if len(ub) < n {
		n = len(ub)
	}
	for i := 0; i < n; i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func (p *canonParser) skipSpace() {
	for p.i < len(p.data) {
		switch p.data[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

// ParseSchemaObject unmarshals a schema for tests and step helpers.
func ParseSchemaObject(raw json.RawMessage) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, invalidArgf("parse schema: %v", err)
	}
	return m, nil
}
