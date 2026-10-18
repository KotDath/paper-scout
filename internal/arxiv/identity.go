// Package arxiv provides public, versioned metadata from the arXiv Atom API.
package arxiv

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var idPattern = regexp.MustCompile(`^([0-9]{4}\.[0-9]{4,5}|[a-z][a-z-]*(?:\.[A-Z]{2})?/[0-9]{7})(?:v([1-9][0-9]*))?$`)

type Identifier struct {
	ArxivID string `json:"arxiv_id"`
	// Zero means latest requested, never a persisted paper version.
	Version int `json:"version"`
}

func (id Identifier) String() string {
	if id.Version == 0 {
		return id.ArxivID
	}
	return fmt.Sprintf("%sv%d", id.ArxivID, id.Version)
}

// ParseID accepts bare IDs, arXiv: IDs, and official abs/pdf/html URLs. No
// caller-supplied URL is fetched; canonical links are built from this ID.
func ParseID(raw string) (Identifier, error) {
	bad := errors.New("invalid arXiv identifier")
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(s), "arxiv:") {
		s = s[len("arxiv:"):]
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") ||
			(u.Host != "arxiv.org" && u.Host != "export.arxiv.org") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return Identifier{}, bad
		}
		parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
		if len(parts) != 2 || (parts[0] != "abs" && parts[0] != "pdf" && parts[0] != "html") {
			return Identifier{}, bad
		}
		s = parts[1]
		if parts[0] == "pdf" {
			s = strings.TrimSuffix(s, ".pdf")
		}
	}
	m := idPattern.FindStringSubmatch(s)
	if m == nil {
		return Identifier{}, bad
	}
	id := Identifier{ArxivID: m[1]}
	if m[2] != "" {
		v, err := strconv.Atoi(m[2])
		if err != nil {
			return Identifier{}, bad
		}
		id.Version = v
	}
	// Validate month, sequence and the transition between identifier formats.
	date, sequence, modern := "", "", !strings.Contains(id.ArxivID, "/")
	if modern {
		date, sequence, _ = strings.Cut(id.ArxivID, ".")
	} else {
		archive, digits, _ := strings.Cut(id.ArxivID, "/")
		if base, _, hasClass := strings.Cut(archive, "."); hasClass {
			// These legacy archives permit redundant subject classes. arXiv's
			// API and preferred external identifiers use the archive alone.
			switch base {
			case "math", "cs", "nlin", "q-bio":
				id.ArxivID = base + "/" + digits
			default:
				return Identifier{}, bad
			}
		}
		date, sequence = digits[:4], digits[4:]
	}
	month, _ := strconv.Atoi(date[2:])
	seq, _ := strconv.Atoi(sequence)
	yyMM, _ := strconv.Atoi(date)
	if month < 1 || month > 12 || seq == 0 ||
		(modern && (yyMM < 704 || (yyMM < 1501 && len(sequence) != 4) || (yyMM >= 1501 && len(sequence) != 5))) ||
		(!modern && !(yyMM >= 9107 || yyMM <= 703)) {
		return Identifier{}, bad
	}
	return id, nil
}

func canonicalURL(kind string, id Identifier) string {
	return "https://arxiv.org/" + kind + "/" + id.String()
}
