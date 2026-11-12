package git

import "strings"

// Unified serializes the complete patch, including its metadata preamble.
func (p *Patch) Unified() string {
	var b strings.Builder
	for _, line := range p.Preamble {
		b.WriteString(line + "\n")
	}
	for _, h := range p.Hunks {
		b.WriteString(h.Header + "\n")
		for _, line := range h.Lines {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
