package logger

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Level represents a log severity level.
type Level int32

const (
	LevelDebug Level = iota // 0 — most verbose
	LevelInfo               // 1 — default
	LevelWarn               // 2
	LevelError              // 3
)

var currentLevel atomic.Int32

func init() {
	currentLevel.Store(int32(LevelInfo))
	if home, err := os.UserHomeDir(); err == nil {
		RegisterPrivatePath(home, "[HOME]")
	}
	RegisterPrivatePath(os.TempDir(), "[TMP]")
}

// SetLevel sets the minimum level written to the log. Safe to call from any goroutine.
func SetLevel(l Level) {
	currentLevel.Store(int32(l))
}

// LevelFromString maps a string name to a Level.
// Recognised values (case-insensitive): "debug", "info", "warn", "error".
// Empty or unknown strings resolve to LevelInfo.
func LevelFromString(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

type secret struct {
	plain string
	label string
}

type privatePath struct {
	root  string
	label string
}

var (
	secretsMu sync.RWMutex
	secrets   []secret
	paths     []privatePath

	// Query credentials occur in itch.io resolver URLs and CDN-signed URLs.
	// Preserve parameter names for diagnosis, but never their values.
	sensitiveQueryValue = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|key|csrf(?:_token)?|token|access[_-]?token|refresh[_-]?token|signature|x-amz-signature|x-amz-credential|x-amz-security-token|awsaccesskeyid|googleaccessid|policy|download_key(?:_id)?|purchase[_-]?token)=)[^&\s"'<>]+`)
	// Free-download page URLs carry the download key as a path segment rather
	// than a query parameter.
	signedDownloadPath = regexp.MustCompile(`(?i)(https?://[^\s"'<>]+/download/)[^/?\s"'<>]+`)
	// Header-shaped values can surface through wrapped HTTP errors even though
	// the app never logs request/response headers intentionally.
	authorizationValue = regexp.MustCompile(`(?i)((?:authorization|proxy-authorization)\s*[:=]\s*(?:\[\s*)?(?:bearer|basic)?\s*)[^\]\s,;}"']+`)
	cookieValue        = regexp.MustCompile(`(?i)((?:cookie|set-cookie)\s*[:=]\s*(?:\[\s*)?)[^\]\r\n}]+`)
	// Resolver/API error bodies may use JSON or key=value rather than URLs.
	structuredSecret = regexp.MustCompile(`(?i)((?:api[_-]?key|download[_-]?key|purchase[_-]?token|csrf[_-]?token|access[_-]?token|refresh[_-]?token)\s*[:=]\s*["']?)[^\s,;}\]"']+`)
	jsonSecret       = regexp.MustCompile(`(?i)(["'](?:api[_-]?key|download[_-]?key|purchase[_-]?token|csrf[_-]?token|access[_-]?token|refresh[_-]?token|authorization|cookie)["']\s*:\s*["'])[^"']+`)
)

// RegisterSecret registers a plaintext value to be fully replaced with label in
// all future log output. Calling with an empty value is a no-op.
// If a secret with the same label already exists, its plaintext is updated.
// Safe to call from any goroutine.
func RegisterSecret(value, label string) {
	if value == "" {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for i, s := range secrets {
		if s.label == label {
			secrets[i].plain = value
			return
		}
	}
	secrets = append(secrets, secret{plain: value, label: label})
}

// RemoveSecret forgets the plaintext registered for label. Use this when a
// persisted credential is removed from application state.
func RemoveSecret(label string) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for index, item := range secrets {
		if item.label == label {
			secrets = append(secrets[:index], secrets[index+1:]...)
			return
		}
	}
}

// RegisterPrivatePath replaces a known local root with label in all future log
// output. Longer roots win, so [APP-DATA] can remain more specific than its SD
// root. Root "/" is rejected because it would destroy every absolute path.
func RegisterPrivatePath(root, label string) {
	root = filepath.Clean(strings.TrimSpace(root))
	label = strings.TrimSpace(label)
	if root == "" || root == "." || root == string(filepath.Separator) || label == "" || !filepath.IsAbs(root) {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for index, item := range paths {
		if item.label == label {
			paths[index].root = root
			sort.SliceStable(paths, func(i, j int) bool { return len(paths[i].root) > len(paths[j].root) })
			return
		}
	}
	paths = append(paths, privatePath{root: root, label: label})
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i].root) > len(paths[j].root) })
}

// RemovePrivatePath forgets a registered root label. It is primarily useful
// for isolated tests; production registrations live for the process lifetime.
func RemovePrivatePath(label string) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for index, item := range paths {
		if item.label == label {
			paths = append(paths[:index], paths[index+1:]...)
			return
		}
	}
}

func pathBoundary(value byte) bool {
	return value == '/' || value == '\\' || value == ':' || value == '=' ||
		value == ' ' || value == '\t' || value == '\r' || value == '\n' ||
		value == '"' || value == '\'' || value == '(' || value == ')' ||
		value == '[' || value == ']' || value == '{' || value == '}' ||
		value == '<' || value == '>' || value == ','
}

func replacePrivatePath(value string, item privatePath) string {
	root := item.root
	for start := 0; start < len(value); {
		offset := strings.Index(value[start:], root)
		if offset < 0 {
			break
		}
		index := start + offset
		end := index + len(root)
		leftOK := index == 0 || pathBoundary(value[index-1])
		rightOK := end == len(value) || pathBoundary(value[end])
		if leftOK && rightOK {
			value = value[:index] + item.label + value[end:]
			start = index + len(item.label)
			continue
		}
		start = index + len(root)
	}
	return value
}

func redact(s string) string {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	for _, path := range paths {
		s = replacePrivatePath(s, path)
	}
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec.plain, sec.label)
	}
	s = sensitiveQueryValue.ReplaceAllString(s, `${1}[REDACTED]`)
	s = signedDownloadPath.ReplaceAllString(s, `${1}[REDACTED]`)
	s = authorizationValue.ReplaceAllString(s, `${1}[REDACTED]`)
	s = cookieValue.ReplaceAllString(s, `${1}[REDACTED]`)
	s = structuredSecret.ReplaceAllString(s, `${1}[REDACTED]`)
	s = jsonSecret.ReplaceAllString(s, `${1}[REDACTED]`)
	return s
}

func write(l Level, format string, args ...any) {
	if Level(currentLevel.Load()) > l {
		return
	}
	var tag string
	switch l {
	case LevelDebug:
		tag = "[DEBUG] "
	case LevelInfo:
		tag = "[INFO]  "
	case LevelWarn:
		tag = "[WARN]  "
	case LevelError:
		tag = "[ERROR] "
	default:
		tag = "[UNKNOWN] "
	}
	log.Print(tag + redact(fmt.Sprintf(format, args...)))
}

// Debug logs at DEBUG level. Suppressed when level is INFO (the default).
func Debug(format string, args ...any) { write(LevelDebug, format, args...) }

// Info logs at INFO level.
func Info(format string, args ...any) { write(LevelInfo, format, args...) }

// Warn logs at WARN level.
func Warn(format string, args ...any) { write(LevelWarn, format, args...) }

// Error logs at ERROR level.
func Error(format string, args ...any) { write(LevelError, format, args...) }
