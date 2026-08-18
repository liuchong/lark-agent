package workspace

import (
	"regexp"
	"strings"
)

// MatchGlob reports whether a slash-separated workspace-relative path matches
// a glob that may include **.
func MatchGlob(pattern, name string) bool {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, `\`, "/"))
	name = strings.TrimPrefix(strings.ReplaceAll(name, `\`, "/"), "./")
	if pattern == "" {
		return true
	}
	re, err := globRegexp(pattern)
	if err != nil {
		return false
	}
	if re.MatchString(name) {
		return true
	}
	return re.MatchString(baseName(name))
}

func baseName(name string) string {
	if index := strings.LastIndex(name, "/"); index >= 0 {
		return name[index+1:]
	}
	return name
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(pattern); {
		if index+1 < len(pattern) && pattern[index] == '*' && pattern[index+1] == '*' {
			if index+2 < len(pattern) && pattern[index+2] == '/' {
				builder.WriteString("(?:.*/)?")
				index += 3
				continue
			}
			builder.WriteString(".*")
			index += 2
			continue
		}
		switch pattern[index] {
		case '*':
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
		}
		index++
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}
