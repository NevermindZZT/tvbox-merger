package fetcher

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"tvbox-merger/internal/config"
)

// DetectResult describes the detected format and extracted JSON
type DetectResult struct {
	Format      string // "plain", "jpeg-embedded", "html", "unknown"
	JSONContent []byte
	RawBytes    []byte
	Err         error
}

// DetectFormat reads the raw bytes and detects the TVBox source format
func DetectFormat(raw []byte) DetectResult {
	if len(raw) == 0 {
		return DetectResult{Format: "unknown", Err: fmt.Errorf("empty content")}
	}

	// Check JPEG header
	if len(raw) > 4 && raw[0] == 0xFF && raw[1] == 0xD8 &&
		(raw[2] == 0xFF || raw[3] == 0xE0 || raw[3] == 0xE1) {
		return extractJPEGEmbeddedJSON(raw)
	}

	// Check if it's HTML
	firstBytes := string(raw[:min(100, len(raw))])
	trimmed := strings.TrimSpace(firstBytes)
	if strings.HasPrefix(trimmed, "<") || strings.HasPrefix(trimmed, "<!") ||
		strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<!DOCTYPE") {
		return DetectResult{Format: "html", RawBytes: raw, Err: fmt.Errorf("source returned HTML, not JSON")}
	}

	// Check if it starts with { — plain JSON
	firstNonSpace := findFirstNonSpace(raw)
	if firstNonSpace < len(raw) && raw[firstNonSpace] == '{' {
		cleaned := stripJSComments(raw)
		if json.Valid(cleaned) {
			return DetectResult{Format: "plain", JSONContent: cleaned, RawBytes: raw}
		}
		// Try strict=false parsing — just return as-is, the merger will handle
		return DetectResult{Format: "plain", JSONContent: cleaned, RawBytes: raw}
	}

	return DetectResult{Format: "unknown", RawBytes: raw, Err: fmt.Errorf("unrecognized format")}
}

// extractJPEGEmbeddedJSON finds JPEG end marker FF D9 and extracts base64 JSON after it
func extractJPEGEmbeddedJSON(raw []byte) DetectResult {
	// Find last FF D9 (JPEG end marker)
	jpegEnd := -1
	for i := 0; i < len(raw)-1; i++ {
		if raw[i] == 0xFF && raw[i+1] == 0xD9 {
			jpegEnd = i + 2 // after the FF D9
		}
	}

	if jpegEnd < 0 || jpegEnd >= len(raw) {
		return DetectResult{Format: "jpeg-embedded", Err: fmt.Errorf("JPEG end marker not found")}
	}

	afterJpeg := raw[jpegEnd:]

	// Trim whitespace and non-base64 characters at start
	str := string(afterJpeg)
	str = strings.TrimSpace(str)

	// Find where base64 JSON starts — look for known base64 prefixes
	// JSON encoded in base64 starts with "ew" ({"...}) or "ey" ({"...})
	b64Start := -1
	lower := strings.ToLower(str)
	if idx := strings.Index(lower, "ew0k"); idx >= 0 {
		b64Start = idx
	} else if idx := strings.Index(lower, "eyj"); idx >= 0 {
		b64Start = idx
	} else {
		// Try to find any valid base64 sequence
		re := regexp.MustCompile(`[A-Za-z0-9+/=]{100,}`)
		match := re.FindStringIndex(str)
		if match != nil {
			b64Start = match[0]
		}
	}

	if b64Start < 0 {
		return DetectResult{Format: "jpeg-embedded", Err: fmt.Errorf("base64 JSON not found after JPEG")}
	}

	b64Str := str[b64Start:]

	// Clean: remove all whitespace and non-base64 trailer
	b64Str = strings.TrimSpace(b64Str)
	b64Str = regexp.MustCompile(`\s+`).ReplaceAllString(b64Str, "")

	// Pad to multiple of 4
	if pad := len(b64Str) % 4; pad != 0 {
		b64Str += strings.Repeat("=", 4-pad)
	}

	decoded, err := base64.StdEncoding.DecodeString(b64Str)
	if err != nil {
		return DetectResult{Format: "jpeg-embedded", Err: fmt.Errorf("base64 decode failed: %w", err)}
	}

	decoded = stripJSComments(decoded)
	return DetectResult{
		Format:      "jpeg-embedded",
		JSONContent: decoded,
		RawBytes:    raw,
		Err:         nil,
	}
}

// stripJSComments removes JavaScript-style // comments from JSON content
func stripJSComments(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Remove inline // comments, but be careful with strings containing //
		cleaned := removeInlineComment(line)
		result = append(result, cleaned)
	}
	return []byte(strings.Join(result, "\n"))
}

func removeInlineComment(line string) string {
	inString := false
	for i := 0; i < len(line); i++ {
		if line[i] == '"' && (i == 0 || line[i-1] != '\\') {
			inString = !inString
		}
		if !inString && i+1 < len(line) && line[i] == '/' && line[i+1] == '/' {
			return line[:i]
		}
	}
	return line
}

func findFirstNonSpace(raw []byte) int {
	for i, b := range raw {
		if !isSpace(b) {
			return i
		}
	}
	return len(raw)
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// FetchResult holds the result of fetching a source
type FetchResult struct {
	SourceID    uint
	SourceName  string
	SourceURL   string
	JSONContent []byte
	RawBytes    []byte
	StatusCode  int
	Format      string
	Cached      bool // true if served from cache
	Healthy     bool
	Err         error
}

// Fetcher handles HTTP fetching of TVBox sources
type Fetcher struct {
	client *http.Client
	cfg    *config.Config
}

func New(cfg *config.Config) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		cfg: cfg,
	}
}

// Fetch downloads a source URL and extracts JSON content
func (f *Fetcher) Fetch(url string) FetchResult {
	result := FetchResult{
		SourceURL: url,
		Healthy:   false,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		result.Err = fmt.Errorf("request creation failed: %w", err)
		return result
	}

	req.Header.Set("User-Agent", f.cfg.DefaultUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := f.client.Do(req)
	if err != nil {
		result.Err = fmt.Errorf("fetch failed: %w", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		result.Err = fmt.Errorf("HTTP %d", resp.StatusCode)
		return result
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024)) // 50MB limit
	if err != nil {
		result.Err = fmt.Errorf("read body failed: %w", err)
		return result
	}

	result.RawBytes = raw

	// Detect format
	detect := DetectFormat(raw)
	result.Format = detect.Format
	result.JSONContent = detect.JSONContent

	if detect.Err != nil {
		result.Err = detect.Err
		return result
	}

	result.Healthy = true
	return result
}
