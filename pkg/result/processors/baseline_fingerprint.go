package processors

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	baselineHashLength = 8
	byteOrderMark      = "\xef\xbb\xbf"
)

var (
	// Positions of other code embedded in a message move when the file is edited.
	baselineMessagePosition = regexp.MustCompile(`(\.go|go\.mod|go\.work):\d+(?:[-:]\d+)*`)
	baselineMessageLines    = regexp.MustCompile(`\blines? \d+(?:-\d+)?`)
	baselineMessageSpan     = regexp.MustCompile(`\b\d+-\d+ lines\b`)
)

// baselineNormalizeLine trims a line and collapses every whitespace run to one space.
func baselineNormalizeLine(line string) string {
	return strings.Join(strings.Fields(strings.TrimPrefix(line, byteOrderMark)), " ")
}

// baselineLineHash fingerprints the source line of an issue.
// Formatting and line-ending changes keep the fingerprint, an edit of the text changes it.
func baselineLineHash(line string) string {
	normalized := baselineNormalizeLine(line)
	if normalized == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(normalized))

	return hex.EncodeToString(sum[:])[:baselineHashLength]
}

// baselineNormalizeMessage strips what changes without the flagged code changing:
// the base path and the line numbers that are part of the message.
func baselineNormalizeMessage(message, basePath string) string {
	if basePath != "" {
		message = strings.ReplaceAll(message, strings.TrimSuffix(basePath, "/")+"/", "")
	}

	message = baselineMessagePosition.ReplaceAllString(message, "$1:N")
	message = baselineMessageSpan.ReplaceAllString(message, "N lines")

	return baselineMessageLines.ReplaceAllString(message, "line N")
}
