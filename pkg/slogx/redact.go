package slogx

import (
	"encoding/json"
	"log/slog"
	"strings"
)

const DefaultMask = "[REDACTED]"

var DefaultSensitiveKeys = []string{
	"password", "passwd", "pass", "password_confirmation", "confirm_password", "password_hash",
	"token", "token_hash", "access_token", "refresh_token", "auth_token",
	"api_key", "apikey", "secret", "api_secret", "client_secret",
	"jwt", "jwt_secret", "authorization", "bearer", "cookie",
	"session_id", "session_token", "private_key", "public_key",
	"otp", "verification_code", "passphrase",
	"ssn", "credit_card", "card_number", "cvv",
	"connection_string", "database_url", "db_url",
	"aws_access_key_id", "aws_secret_access_key", "aws_session_token",
}

var defaultSensitiveSet = keySet(DefaultSensitiveKeys, nil)

func RedactAttr(keys, extraKeys []string) func(groups []string, a slog.Attr) slog.Attr {
	set := defaultSensitiveSet
	if keys != nil || extraKeys != nil {
		base := keys
		if base == nil {
			base = DefaultSensitiveKeys
		}
		set = keySet(base, extraKeys)
	}

	return func(_ []string, a slog.Attr) slog.Attr {
		if _, sensitive := set[strings.ToLower(a.Key)]; sensitive {
			return slog.String(a.Key, DefaultMask)
		}
		if a.Value.Kind() != slog.KindAny {
			return a
		}
		value := a.Value.Any()
		if value == nil {
			return a
		}
		if _, isErr := value.(error); isErr {
			return a
		}
		if redacted, ok := redactValue(value, set); ok {
			return slog.Attr{Key: a.Key, Value: slog.AnyValue(redacted)}
		}
		return a
	}
}

func RedactMap(m map[string]any) map[string]any {
	return redactMap(m, defaultSensitiveSet)
}

func RedactJSON(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return b, nil
	}
	var data any
	if err := json.Unmarshal(b, &data); err != nil {
		return b, err
	}
	redacted, ok := redactValue(data, defaultSensitiveSet)
	if !ok {
		return b, nil
	}
	return json.Marshal(redacted)
}

func keySet(keys, extraKeys []string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys)+len(extraKeys))
	for _, k := range append(append([]string{}, keys...), extraKeys...) {
		set[strings.ToLower(k)] = struct{}{}
	}
	return set
}

func redactValue(v any, set map[string]struct{}) (any, bool) {
	switch value := v.(type) {
	case map[string]any:
		return redactMap(value, set), true
	case []any:
		return redactSlice(value, set), true
	default:
		return nil, false
	}
}

func redactMap(m map[string]any, set map[string]struct{}) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if _, sensitive := set[strings.ToLower(k)]; sensitive {
			out[k] = DefaultMask
			continue
		}
		if redacted, ok := redactValue(v, set); ok {
			out[k] = redacted
			continue
		}
		out[k] = v
	}
	return out
}

func redactSlice(s []any, set map[string]struct{}) []any {
	out := make([]any, len(s))
	for i, item := range s {
		if redacted, ok := redactValue(item, set); ok {
			out[i] = redacted
			continue
		}
		out[i] = item
	}
	return out
}
