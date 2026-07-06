package userlog

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sud0x0/go-api-template/internal/shared"
)

// Cursor format: base64url(<RFC3339Nano date_and_time>|<uuid id>) of the last
// row of a page. It is OPAQUE to clients; they echo it back verbatim as the
// `cursor` query parameter to fetch the next page. The two parts are the keyset
// sort key (date_and_time, id) that the cursor query resumes after.

// encodeCursor builds the opaque cursor for a page's last row. The timestamp is
// formatted in UTC with nanosecond precision so it round-trips losslessly and
// orders identically to the stored TIMESTAMPTZ.
func encodeCursor(dateAndTime time.Time, id string) string {
	raw := dateAndTime.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor validates and decodes a client-supplied cursor into its keyset
// components. BOTH parts are validated strictly: the timestamp via
// RFC3339Nano, the id via uuid.Parse (then canonicalised to the bare lowercase
// form Postgres' uuid type accepts). Any malformed input returns
// shared.ErrInvalidPagination, which the handler maps to 400.
//
// SECURITY: validation here is NOT an authorisation control. A forged or
// tampered cursor cannot cross user boundaries because every list query remains
// scoped by `WHERE user_id = $1`; the worst a forged cursor can do is select a
// different point in the CALLER'S OWN data. Strict validation exists only to
// keep a cast-error 500 (a non-UUID or non-timestamp reaching Postgres) and
// pathological values out of SQL. Never let cursor contents reach the query
// unvalidated.
func decodeCursor(raw string) (time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", shared.ErrInvalidPagination
	}
	before, after, found := strings.Cut(string(decoded), "|")
	if !found {
		return time.Time{}, "", shared.ErrInvalidPagination
	}
	ts, err := time.Parse(time.RFC3339Nano, before)
	if err != nil {
		return time.Time{}, "", shared.ErrInvalidPagination
	}
	id, err := uuid.Parse(after)
	if err != nil {
		return time.Time{}, "", shared.ErrInvalidPagination
	}
	return ts, id.String(), nil
}
