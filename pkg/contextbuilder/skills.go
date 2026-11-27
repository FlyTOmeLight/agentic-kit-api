package contextbuilder

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	MaxSkillDirectoryEntries = 16
	maxSkillDirectoryBytes   = 8 << 10
)

// RenderSkillDirectory builds a compact progressive-disclosure catalog of mounted
// skill packages so the model sees only names, descriptions, and available helper
// file paths until it calls load_skill.
func RenderSkillDirectory(skills []SkillSummary) (PinnedBlock, bool) {
	if len(skills) == 0 {
		return PinnedBlock{}, false
	}
	var b strings.Builder
	b.WriteString("Use load_skill(name, path?) to read a mounted skill's SKILL.md or supporting files on demand:\n")
	count := 0
	for _, skill := range skills {
		if count >= MaxSkillDirectoryEntries || skill.Name == "" {
			break
		}
		var line strings.Builder
		fmt.Fprintf(&line, "- %s", skill.Name)
		if skill.Description != "" {
			fmt.Fprintf(&line, ": %s", skill.Description)
		}
		var helpers []string
		entry := skill.EntryPath
		if entry == "" {
			entry = "SKILL.md"
		}
		for _, f := range skill.Files {
			if f != "" && f != entry {
				helpers = append(helpers, f)
			}
		}
		if len(helpers) > 0 {
			const maxShownHelpers = 8
			shown := helpers
			extra := 0
			if len(shown) > maxShownHelpers {
				extra = len(shown) - maxShownHelpers
				shown = shown[:maxShownHelpers]
			}
			line.WriteString(" [files: ")
			for i, h := range shown {
				if i > 0 {
					line.WriteString(", ")
				}
				line.WriteString(strconv.Quote(h))
			}
			if extra > 0 {
				fmt.Fprintf(&line, ", +%d more", extra)
			}
			line.WriteByte(']')
		}
		line.WriteByte('\n')
		remaining := 0
		for _, rest := range skills {
			if rest.Name != "" {
				remaining++
			}
		}
		omitted := remaining - count
		reserve := 0
		if omitted > 1 {
			reserve = len(fmt.Sprintf("(+%d more skills omitted)\n", omitted-1))
		}
		if b.Len()+line.Len()+reserve > maxSkillDirectoryBytes {
			break
		}
		b.WriteString(line.String())
		count++
	}
	if count == 0 {
		return PinnedBlock{}, false
	}
	totalValid := 0
	for _, skill := range skills {
		if skill.Name != "" {
			totalValid++
		}
	}
	if omitted := totalValid - count; omitted > 0 {
		note := fmt.Sprintf("(+%d more skills omitted)\n", omitted)
		if b.Len()+len(note) <= maxSkillDirectoryBytes {
			b.WriteString(note)
		}
	}
	return PinnedBlock{Label: "skills", Content: strings.TrimSuffix(b.String(), "\n")}, true
}
