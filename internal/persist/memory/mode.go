package memory

import "strings"

// Session memory modes. Normal reads and writes. Incognito reads what is
// already stored and refuses new writes. Temporary neither reads nor writes.
const (
	ModeNormal    = "normal"
	ModeIncognito = "incognito"
	ModeTemporary = "temporary"
)

// NormalizeMode accepts empty as normal. Unknown values return an error
// string in the second result; the mode is left empty.
func NormalizeMode(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", ModeNormal:
		return ModeNormal, nil
	case ModeIncognito:
		return ModeIncognito, nil
	case ModeTemporary:
		return ModeTemporary, nil
	default:
		return "", errUnknownMode(strings.TrimSpace(s))
	}
}

type modeError string

func errUnknownMode(s string) error { return modeError("memory mode " + s + " is unknown") }

func (e modeError) Error() string { return string(e) }

// AllowsRead reports whether the mode may load memory into a prompt or tool.
func AllowsRead(mode string) bool {
	m, err := NormalizeMode(mode)
	return err == nil && m != ModeTemporary
}

// AllowsWrite reports whether the mode may persist memory or knowledge.
func AllowsWrite(mode string) bool {
	m, err := NormalizeMode(mode)
	return err == nil && m == ModeNormal
}
