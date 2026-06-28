package pipeline

import "fmt"

// conventionalType maps a semver bump to a Conventional Commits type + breaking flag; ("", false) for an empty/"none" bump.
func conventionalType(bump string) (typ string, breaking bool) {
	switch bump {
	case "patch":
		return "fix", false
	case "minor":
		return "feat", false
	case "major":
		return "feat", true
	default:
		return "", false
	}
}

// conventionalSubject formats a CC subject with the ticket ID suffixed, e.g. "feat: add X (ENG-42)" / "feat!: drop Y (ENG-42)".
func conventionalSubject(typ string, breaking bool, title, id string) string {
	bang := ""
	if breaking {
		bang = "!"
	}
	return fmt.Sprintf("%s%s: %s (%s)", typ, bang, title, id)
}
