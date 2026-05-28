package authhandler

import (
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// LDAPConfig holds the connection and search parameters for an LDAP provider.
type LDAPConfig struct {
	URL             string `json:"url"`
	BindDN          string `json:"bindDN"`
	BindCredentials string `json:"bindCredentials"`
	SearchBase      string `json:"searchBase"`
	SearchFilter    string `json:"searchFilter"`
}

// LDAPResult carries the outcome of an LDAP authentication attempt.
type LDAPResult struct {
	Success   bool
	FirstName string
	LastName  string
	Email     string
}

// AuthenticateLDAP authenticates username/password against an LDAP server
// described by config. Returns nil on connection/bind errors; returns a
// LDAPResult with Success=false when the credentials are wrong.
func AuthenticateLDAP(username, password string, config LDAPConfig) *LDAPResult {
	// Connect with a 5-second dial timeout.
	conn, err := ldap.DialURL(config.URL, ldap.DialWithDialer(&net.Dialer{
		Timeout: 5 * time.Second,
	}))
	if err != nil {
		slog.Warn("ldap dial failed", "url", config.URL, "err", err)
		return nil
	}
	defer conn.Close()

	// Bind with the service account.
	if err = conn.Bind(config.BindDN, config.BindCredentials); err != nil {
		slog.Warn("ldap service bind failed", "bindDN", config.BindDN, "err", err)
		return nil
	}

	// Search for the user entry.
	filter := strings.ReplaceAll(config.SearchFilter, "{{username}}", escapeLDAPFilter(username))
	searchReq := ldap.NewSearchRequest(
		config.SearchBase,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1, // size limit
		0, // time limit
		false,
		filter,
		[]string{"dn", "eduPersonNickname", "givenName", "cn", "sn", "mail"},
		nil,
	)

	sr, err := conn.Search(searchReq)
	if err != nil {
		slog.Warn("ldap search failed", "base", config.SearchBase, "filter", filter, "err", err)
		return &LDAPResult{Success: false}
	}
	if len(sr.Entries) == 0 {
		slog.Warn("ldap search returned no entries", "base", config.SearchBase, "filter", filter)
		return &LDAPResult{Success: false}
	}

	entry := sr.Entries[0]
	userDN := entry.DN

	// Rebind as the user.
	if err = conn.Bind(userDN, password); err != nil {
		slog.Warn("ldap user bind failed", "userDN", userDN, "err", err)
		return &LDAPResult{Success: false}
	}

	// Extract attributes.
	firstName := firstNonEmpty(
		entry.GetAttributeValue("eduPersonNickname"),
		entry.GetAttributeValue("givenName"),
		entry.GetAttributeValue("cn"),
	)
	lastName := entry.GetAttributeValue("sn")
	email := entry.GetAttributeValue("mail")

	return &LDAPResult{
		Success:   true,
		FirstName: firstName,
		LastName:  lastName,
		Email:     email,
	}
}

// escapeLDAPFilter escapes special characters in an LDAP filter value per RFC 4515.
func escapeLDAPFilter(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch c {
		case '\\':
			b.WriteString(`\5c`)
		case '*':
			b.WriteString(`\2a`)
		case '(':
			b.WriteString(`\28`)
		case ')':
			b.WriteString(`\29`)
		case '[':
			b.WriteString(`\5b`)
		case ']':
			b.WriteString(`\5d`)
		case 0:
			b.WriteString(`\00`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// firstNonEmpty returns the first non-empty string from the provided values.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
