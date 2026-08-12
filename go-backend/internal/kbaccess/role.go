package kbaccess

// KB-Rollen, streng geordnet. Handler und Middleware verwenden ausschliesslich
// diese Konstanten — ein Tippfehler soll ein Compile-Fehler sein.
const (
	RoleView  = "view"
	RoleEdit  = "edit"
	RoleAdmin = "admin"
	RoleOwner = "owner"
)

// roleRank ordnet die Rollen. Nicht exportiert, damit die Ordnung nur ueber
// Rank/AtLeast befragt wird und nirgends dupliziert entsteht.
var roleRank = map[string]int{
	RoleView:  0,
	RoleEdit:  1,
	RoleAdmin: 2,
	RoleOwner: 3,
}

// Rank returns the ordinal of role, or -1 when role is not a known KB role.
func Rank(role string) int {
	if r, ok := roleRank[role]; ok {
		return r
	}
	return -1
}

// AtLeast reports whether granted meets or exceeds required. An unknown value
// on either side yields false — an unparseable role never satisfies a gate.
func AtLeast(granted, required string) bool {
	g, r := Rank(granted), Rank(required)
	return g >= 0 && r >= 0 && g >= r
}

// Valid reports whether role is one of the four KB roles.
func Valid(role string) bool { return Rank(role) >= 0 }

// Assignable reports whether role may be granted through the member endpoints.
// Ownership is deliberately excluded: it moves only via the explicit transfer,
// which also has to maintain the single-owner invariant.
func Assignable(role string) bool {
	return role == RoleView || role == RoleEdit || role == RoleAdmin
}
