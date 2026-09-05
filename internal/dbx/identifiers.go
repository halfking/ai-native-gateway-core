package dbx

import (
	"regexp"

	"github.com/jackc/pgx/v5"
)

// identRe is the whitelist for table and column identifiers: lowercase
// ASCII letters, digits and underscores, starting with a letter/underscore,
// max 63 bytes (PostgreSQL NAMEDATALEN - 1).
var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidateIdentifier enforces the identifier whitelist. Identifiers that do
// not match can never reach SQL text.
func ValidateIdentifier(s string) error {
	if !identRe.MatchString(s) {
		return fieldError(ErrInvalidIdentifier, "", s)
	}
	return nil
}

// QuoteIdentifier validates then quotes an identifier using pgx sanitizing.
// The returned text is safe to embed in SQL.
func QuoteIdentifier(s string) (string, error) {
	if err := ValidateIdentifier(s); err != nil {
		return "", err
	}
	return pgx.Identifier{s}.Sanitize(), nil
}
