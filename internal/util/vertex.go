package util

import (
	"fmt"
	"strings"
)

func SanitizeFilePart(s string) string {
	out := strings.TrimSpace(s)
	replacers := []string{"/", "_", "\\", "_", ":", "_", " ", "-"}
	for i := 0; i < len(replacers); i += 2 {
		out = strings.ReplaceAll(out, replacers[i], replacers[i+1])
	}
	if out == "" {
		return "default"
	}
	return out
}

func LabelForVertex(projectID, email string) string {
	p := strings.TrimSpace(projectID)
	e := strings.TrimSpace(email)
	if p != "" && e != "" {
		return fmt.Sprintf("%s (%s)", p, e)
	}
	if p != "" {
		return p
	}
	if e != "" {
		return e
	}
	return "vertex"
}
