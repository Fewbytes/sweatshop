// Package symbol maps a source location to the name of its enclosing
// declaration. Fingerprints are built on that name rather than a line number,
// so a finding survives edits elsewhere in the file.
package symbol

// Resolver names the declaration enclosing a source line.
type Resolver interface {
	// Enclosing returns the declaration name containing line, and whether one
	// was found. Lines outside any declaration, and files that cannot be
	// parsed, return false.
	Enclosing(file string, line int) (string, bool)
}

// Resolve calls r.Enclosing and flattens the miss case to an empty string.
func Resolve(r Resolver, file string, line int) string {
	name, ok := r.Enclosing(file, line)
	if !ok {
		return ""
	}
	return name
}
