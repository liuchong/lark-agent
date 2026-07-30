package runtime

import (
	"regexp"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

var repositoryPathReferencePattern = regexp.MustCompile(
	`(?:[A-Za-z_.-][A-Za-z0-9._-]*/)+[A-Za-z_.-][A-Za-z0-9._-]*`,
)

var conventionalRootRepositoryPathPattern = regexp.MustCompile(
	`\b(?:Makefile|Dockerfile|Containerfile|go\.mod|go\.sum|Cargo\.toml|build\.zig|build\.zig\.zon)\b`,
)

var lowerCamelIdentifierPattern = regexp.MustCompile(
	`\b[a-z][a-z0-9]*(?:[A-Z][A-Za-z0-9]*)+\b`,
)

var codeIdentifierPattern = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)

var concreteJSONObjectPattern = regexp.MustCompile(
	`\{\s*["'][^"']+["']\s*:`,
)

var concreteJSONArrayPattern = regexp.MustCompile(
	`\[\s*(?:\{|["']|-?[0-9]|true\b|false\b|null\b)`,
)

var serializerOperationPattern = regexp.MustCompile(
	`(?i)(?:json\.(?:marshal|dumps|stringify|encode|newencoder)|` +
		`objectmapper|gson|jsonobject|jsonarray|encodeToString|decodeFromString|` +
		`marshalJSON|unmarshalJSON)`,
)

var inlineJSONObjectPattern = regexp.MustCompile(`\{[^{}\n]*:[^{}\n]*\}`)

func verifyGroundedCodingReply(
	question string,
	decision domain.Decision,
	authoritativeContents map[string]string,
) error {
	if decision.EvidenceStatus != domain.EvidenceVerified {
		return nil
	}
	citedContents := make([]string, 0, len(decision.Sources))
	readSources := make([]domain.SourceRef, 0, len(decision.Sources))
	for _, source := range decision.Sources {
		if content, ok := authoritativeContents[sourceKey(source)]; ok {
			readSources = append(readSources, source)
			citedContents = append(citedContents, content)
		}
	}
	combinedContent := strings.Join(citedContents, "\n")
	citedIdentifiers := identifierSet(combinedContent)
	replyWithoutPaths := decision.ReplyText
	pathLocations := repositoryPathReferencePattern.FindAllStringIndex(decision.ReplyText, -1)
	pathLocations = append(
		pathLocations,
		conventionalRootRepositoryPathPattern.FindAllStringIndex(decision.ReplyText, -1)...,
	)
	for _, location := range pathLocations {
		candidate := decision.ReplyText[location[0]:location[1]]
		if !looksLikeRepositoryPath(
			decision.ReplyText,
			location[0],
			candidate,
			readSources,
		) {
			continue
		}
		if !decisionCitesRepositoryPath(readSources, candidate) {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"verified coding reply contains repository path %s that is not backed by a current-run read; read and cite it or remove it",
				candidate,
			)
		}
		replyWithoutPaths = replyWithoutPaths[:location[0]] +
			strings.Repeat(" ", location[1]-location[0]) +
			replyWithoutPaths[location[1]:]
	}
	seen := make(map[string]bool)
	for _, identifier := range lowerCamelIdentifierPattern.FindAllString(
		replyWithoutPaths,
		-1,
	) {
		if seen[identifier] {
			continue
		}
		seen[identifier] = true
		if citedIdentifiers[strings.ToLower(identifier)] {
			continue
		}
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"verified coding reply identifier %s is absent from all cited authoritative reads; cite supporting evidence or correct the identifier",
			identifier,
		)
	}
	if asksConcreteSerializedShape(question) {
		normalizedContent := normalizeEscapedSerializationText(combinedContent)
		if !hasStructuralSerializationEvidence(normalizedContent) {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"verified coding reply claims a concrete serialized shape but cited reads contain only opaque declarations; read and cite a current example, protocol, or serializer",
			)
		}
		if unsupported := unsupportedConcreteJSONExample(
			normalizeEscapedSerializationText(decision.ReplyText),
			normalizedContent,
		); unsupported != "" {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"verified coding reply contains unsupported serialized example %s; cite the exact current-run evidence or correct it",
				unsupported,
			)
		}
	}
	return nil
}

func looksLikeRepositoryPath(
	text string,
	start int,
	candidate string,
	readSources []domain.SourceRef,
) bool {
	if repositoryPathIsURL(text, start) ||
		(start > 0 && text[start-1] == '/') {
		return false
	}
	if conventionalRootRepositoryPathPattern.MatchString(candidate) ||
		strings.Contains(filepathBase(candidate), ".") {
		return true
	}
	candidateRoot := repositoryPathRoot(candidate)
	if candidateRoot == "" {
		return false
	}
	for _, source := range readSources {
		if strings.EqualFold(
			repositoryPathRoot(source.RelativePath),
			candidateRoot,
		) {
			return true
		}
	}
	return false
}

func filepathBase(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func repositoryPathRoot(path string) string {
	path = strings.TrimPrefix(path, "./")
	if index := strings.Index(path, "/"); index >= 0 {
		return path[:index]
	}
	return ""
}

func identifierSet(content string) map[string]bool {
	identifiers := make(map[string]bool)
	for _, identifier := range codeIdentifierPattern.FindAllString(content, -1) {
		identifiers[strings.ToLower(identifier)] = true
	}
	return identifiers
}

func asksConcreteSerializedShape(question string) bool {
	question = strings.ToLower(question)
	hasShapeIntent := containsAny(
		question,
		"结构",
		"格式",
		"形状",
		"长什么样",
		"怎么传",
		"如何传",
		"具体内容",
		"具体值",
		"shape",
		"schema",
		"format",
	)
	hasSerializedTarget := containsAny(
		question,
		"json",
		"序列化",
		"字符串",
		"字节",
		"请求体",
		"payload",
		"body",
		"string",
		"bytes",
		"rawmessage",
		"samplecontent",
	)
	return hasShapeIntent && hasSerializedTarget
}

func normalizeEscapedSerializationText(content string) string {
	return strings.ReplaceAll(content, `\"`, `"`)
}

func hasStructuralSerializationEvidence(content string) bool {
	return concreteJSONObjectPattern.MatchString(content) ||
		concreteJSONArrayPattern.MatchString(content) ||
		serializerOperationPattern.MatchString(content)
}

func unsupportedConcreteJSONExample(reply, evidence string) string {
	compactEvidence := compactSerializationExample(evidence)
	for _, example := range inlineJSONObjectPattern.FindAllString(reply, -1) {
		compactExample := compactSerializationExample(example)
		if compactExample != "" && !strings.Contains(compactEvidence, compactExample) {
			return example
		}
	}
	return ""
}

func compactSerializationExample(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func repositoryPathIsURL(text string, start int) bool {
	prefixStart := start - len("https://")
	if prefixStart < 0 {
		prefixStart = 0
	}
	prefix := strings.ToLower(text[prefixStart:start])
	return strings.HasSuffix(prefix, "http://") ||
		strings.HasSuffix(prefix, "https://")
}

func decisionCitesRepositoryPath(
	sources []domain.SourceRef,
	candidate string,
) bool {
	candidate = strings.TrimPrefix(candidate, "./")
	for _, source := range sources {
		sourcePath := strings.TrimPrefix(source.RelativePath, "./")
		if sourcePath == candidate ||
			strings.HasSuffix(sourcePath, "/"+candidate) {
			return true
		}
	}
	return false
}
