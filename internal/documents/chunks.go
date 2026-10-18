package documents

import (
	"crypto/sha256"
	"fmt"
	"unicode"
)

func hash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func makeChunks(d Document) []Chunk {
	result := []Chunk{}
	text := []rune(d.Text)
	for _, segment := range d.Segments {
		for start := segment.CharStart; start < segment.CharEnd; {
			end := min(start+2000, segment.CharEnd)
			// Prefer a paragraph or whitespace boundary without dropping any characters.
			if end < segment.CharEnd {
				for i := end; i > start+1000; i-- {
					if text[i-1] == '\n' {
						end = i
						break
					}
				}
				if end == start+2000 {
					for i := end; i > start+1500; i-- {
						if unicode.IsSpace(text[i-1]) {
							end = i
							break
						}
					}
				}
			}
			body := string(text[start:end])
			ordinal := len(result)
			id := "ev_" + hash([]byte(fmt.Sprintf("%s:%d:%d:%d", d.ID, ordinal, start, end)))
			result = append(result, Chunk{ID: id, DocumentID: d.ID, Ordinal: ordinal, Text: body,
				CharStart: start, CharEnd: end, PageStart: segment.Page, PageEnd: segment.Page, Section: segment.Section})
			if end == segment.CharEnd {
				break
			}
			start = end - 250
		}
	}
	return result
}

type textPart struct {
	text    string
	page    int
	section string
}

func assemble(parts []textPart) (string, []Segment) {
	all := []rune{}
	segments := []Segment{}
	for i, part := range parts {
		start := len(all)
		all = append(all, []rune(part.text)...)
		// Separators belong to the preceding segment, so coverage is contiguous.
		if part.text != "" && i < len(parts)-1 {
			all = append(all, '\n', '\n')
		}
		segments = append(segments, Segment{CharStart: start, CharEnd: len(all), Page: part.page, Section: part.section})
	}
	return string(all), segments
}
