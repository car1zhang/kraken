package api

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// jobform_swift_test.go — a regression pin for the iOS job form's save request.
//
// Nothing in CI compiles Swift: `go test` runs on every PR, `xcodebuild` runs
// only when carl ships to TestFlight. So a Swift call site that falls out of
// step with its own model is invisible until the next ship, which is exactly
// what happened on 2026-09-14: `agent` was added to JobFormSaveRequest
// (Models.swift) while JobFormView's one call site kept passing the old seven
// arguments, and the archive died a month later on
//
//	JobFormView.swift: error: missing argument for parameter 'agent' in call
//
// This test reads both files off disk and holds two things: every stored
// property of JobFormSaveRequest is a labelled argument at every
// `JobFormSaveRequest(` call site in ios/Kraken, and every wire key the Swift
// request encodes is one jobFormFields (the server's decode) actually reads —
// an extra key would be silently dropped by the server, never refused.

// swiftStoredLet matches one stored property declaration with no default —
// `let name: Type` or `var name: Type` — which is exactly the set the
// memberwise init requires a labelled argument for. A property with a
// `= default` or a `{ get }` body is not a required argument and is skipped.
var swiftStoredLet = regexp.MustCompile(`(?m)^\s*(?:let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*:[^=\n{]*$`)

// swiftCodingCase matches the case lists inside a CodingKeys enum —
// `case a, b, c` and `case a = "wire_name"` alike.
var swiftCodingCase = regexp.MustCompile(`(?m)^\s*case\s+(.+)$`)

// swiftBlock returns the body of `<decl> {` up to its matching brace.
func swiftBlock(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("no %q in source", decl)
	}
	rest := src[i:]
	open := strings.Index(rest, "{")
	if open < 0 {
		t.Fatalf("%q has no body", decl)
	}
	depth := 0
	for j := open; j < len(rest); j++ {
		switch rest[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[open+1 : j]
			}
		}
	}
	t.Fatalf("%q body never closes", decl)
	return ""
}

// swiftLineComment matches a `//` comment to end of line, so a comment that
// mentions `JobFormSaveRequest(` is not read as a call site.
var swiftLineComment = regexp.MustCompile(`(?m)//[^\n]*`)

// swiftCallLabel matches one argument label inside a call's parens.
var swiftCallLabel = regexp.MustCompile(`(?:^|,)\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`)

// swiftCallLabels returns the argument labels of every `<typeName>(` call in
// src, one []string per call, reading to the matching close paren so a
// multi-line call is one call. Line comments are blanked first. A nested
// call's labels or a string literal containing parens would be read too;
// both make the test fail loudly rather than pass, which is the right side.
func swiftCallLabels(src, typeName string) [][]string {
	src = swiftLineComment.ReplaceAllString(src, "")
	var calls [][]string
	needle := typeName + "("
	for i := 0; ; {
		k := strings.Index(src[i:], needle)
		if k < 0 {
			return calls
		}
		start := i + k + len(needle)
		// Walk to the matching close paren, skipping string literals so a
		// paren inside one neither ends the call nor opens a level, and keep
		// only the depth-0 text so a nested call's labels are not read as
		// this call's.
		depth := 1
		end := start
		inString := false
		var top strings.Builder
		for end < len(src) && depth > 0 {
			c := src[end]
			switch {
			case inString:
				if c == '\\' && end+1 < len(src) {
					end++ // the escaped char is part of the literal
				} else if c == '"' {
					inString = false
				}
			case c == '"':
				inString = true
			case c == '(':
				depth++
			case c == ')':
				depth--
			}
			if depth == 1 && !inString && c != '"' {
				top.WriteByte(c)
			}
			end++
		}
		var labels []string
		for _, m := range swiftCallLabel.FindAllStringSubmatch(top.String(), -1) {
			labels = append(labels, m[1])
		}
		calls = append(calls, labels)
		i = end
	}
}

// swiftWithout returns src with the `<decl> { … }` block removed, so a
// struct's stored properties can be read whether its CodingKeys enum sits
// above or below them.
func swiftWithout(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		return src
	}
	inner := swiftBlock(t, src, decl)
	open := i + strings.Index(src[i:], "{")
	return src[:i] + src[open+1+len(inner)+1:]
}

func TestJobFormSaveRequestSwiftCallSitesCarryEveryField(t *testing.T) {
	models, err := os.ReadFile("../../ios/Kraken/Models.swift")
	if os.IsNotExist(err) {
		t.Skip("no ios/Kraken/Models.swift in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	body := swiftBlock(t, string(models), "struct JobFormSaveRequest")
	// The stored properties are the init's parameters, in order; the nested
	// CodingKeys enum is cut away first so its cases are not read as lets.
	if !strings.Contains(body, "enum CodingKeys") {
		t.Fatal("JobFormSaveRequest declares no CodingKeys — the wire-key half of this test has nothing to read")
	}
	props := swiftWithout(t, body, "enum CodingKeys")
	var want []string
	for _, m := range swiftStoredLet.FindAllStringSubmatch(props, -1) {
		want = append(want, m[1])
	}
	if len(want) < 7 {
		t.Fatalf("JobFormSaveRequest declares %d stored properties, expected at least the seven it shipped with: %v", len(want), want)
	}

	files, err := filepath.Glob("../../ios/Kraken/*.swift")
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	for _, f := range files {
		if filepath.Base(f) == "Models.swift" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range swiftCallLabels(string(src), "JobFormSaveRequest") {
			sites++
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: JobFormSaveRequest(...) passes %v, but Models.swift declares %v — the ship's xcodebuild archive is the only other place this is checked", filepath.Base(f), got, want)
			}
		}
	}
	if sites == 0 {
		t.Fatal("no JobFormSaveRequest(...) call site in ios/Kraken — the form has nothing to save with")
	}

	// Every wire key the Swift request encodes must be one the server reads.
	coding := swiftBlock(t, body, "enum CodingKeys")
	wire := map[string]bool{}
	for _, m := range swiftCodingCase.FindAllStringSubmatch(coding, -1) {
		for _, c := range strings.Split(m[1], ",") {
			c = strings.TrimSpace(c)
			if eq := strings.Index(c, "="); eq >= 0 {
				c = strings.Trim(strings.TrimSpace(c[eq+1:]), `"`)
			}
			wire[c] = true
		}
	}
	server := map[string]bool{}
	rt := reflect.TypeOf(jobFormFields{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		server[tag] = true
	}
	var missing []string
	for k := range wire {
		if !server[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("JobFormSaveRequest encodes %v, which jobFormFields does not decode — the server drops them silently", missing)
	}
}
