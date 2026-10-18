package documents

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A bounded command sink prevents untrusted PDFs from filling process memory.
type limitedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		return 0, errors.New("extract_output_too_large")
	}
	return w.buffer.Write(p)
}
func (s *Service) command(ctx context.Context, name string, limit int, args ...string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, s.cfg.ExtractTimeout)
	defer cancel()
	cmd := exec.CommandContext(child, name, args...)
	out := &limitedOutput{limit: limit}
	cmd.Stdout = out
	// Poppler diagnostics may contain source paths or PDF content; never expose them.
	if err := cmd.Run(); err != nil {
		if errors.Is(child.Err(), context.DeadlineExceeded) {
			return nil, errors.New(name + "_timeout")
		}
		if child.Err() != nil {
			return nil, child.Err()
		}
		return nil, errors.New(name + "_failed")
	}
	return out.buffer.Bytes(), nil
}
func (s *Service) extractPDF(ctx context.Context, path string) (string, []Segment, []Warning, error) {
	info, err := s.command(ctx, "pdfinfo", 1<<20, path)
	if err != nil {
		return "", nil, nil, err
	}
	pages := 0
	for _, line := range strings.Split(string(info), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			pages, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		}
	}
	if pages < 1 || pages > 2000 {
		return "", nil, nil, errors.New("pdf_page_count_invalid")
	}
	body, err := s.command(ctx, "pdftotext", 32<<20, "-enc", "UTF-8", "-layout", path, "-")
	if err != nil {
		return "", nil, nil, err
	}
	warnings := []Warning{{Code: "pdf_images_formulas_tables_not_guaranteed"}}
	if !utf8.Valid(body) || bytes.Contains(body, []byte("\xef\xbf\xbd")) {
		warnings = append(warnings, Warning{Code: "pdf_text_replacement_characters"})
	}
	raw := strings.Split(strings.ToValidUTF8(string(body), "\uFFFD"), "\f")
	if len(raw) == pages+1 && strings.TrimSpace(raw[len(raw)-1]) == "" {
		raw = raw[:len(raw)-1]
	}
	if len(raw) != pages {
		return "", nil, nil, errors.New("pdf_page_boundaries_mismatch")
	}
	parts := make([]textPart, pages)
	letters := 0
	for i, page := range raw {
		page = strings.TrimSpace(strings.ReplaceAll(page, "\r\n", "\n"))
		parts[i] = textPart{text: page, page: i + 1}
		pageLetters := 0
		for _, r := range page {
			if unicode.IsLetter(r) {
				pageLetters++
			}
		}
		letters += pageLetters
		if pageLetters == 0 {
			warnings = append(warnings, Warning{Code: "pdf_page_without_text", Page: i + 1})
		} else if pageLetters < 40 {
			warnings = append(warnings, Warning{Code: "pdf_page_sparse_text", Page: i + 1})
		}
	}
	if letters < 100 {
		return "", nil, nil, errors.New("pdf_text_unusable")
	}
	text, segments := assemble(parts)
	return text, segments, warnings, nil
}
