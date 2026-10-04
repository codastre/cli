package transcript

import (
	"regexp"
	"strings"
)

// Pipeline structure of a (masked) shell command. This mirrors shell.js in
// the plugin's adapters/claude/hooks (codastre-claude repo); the two share
// testdata/bash-classify-fixtures.json, so change both together.
//
// Input is maskQuoted output: quoted bodies are blanked, so every separator
// left is one the shell acts on. A command splits into lists (at ; & && || (
// ) ` and newlines) and each list into pipeline stages (at a single |). A
// grep is a code search only where it reads the repo: at the head of a
// pipeline, or downstream of a stage that reads files. `security … | rg -c x`
// or `git log | grep fix` filters a program's output — not a search Codastre
// could have answered — and used to count as one.

var searchTools = setOf("grep", "rg", "ag", "ack", "fd", "findstr")

// contentSources are pipeline heads whose output is repo content or file
// names, so a grep fed by them is still searching the repo.
var contentSources = setOf("cat", "bat", "tac", "nl", "head", "tail", "less", "more",
	"sed", "awk", "cut", "ls", "tree", "find", "fd")

var gitContent = setOf("ls-files", "ls-tree", "show", "diff", "cat-file", "blame")

// readers are file viewers: a pipeline head that prints a file is a read (as
// the Read tool would be), provided it names one and writes nowhere.
var readers = setOf("cat", "bat", "tac", "nl", "head", "tail", "less", "more")

// stagePrefixes precede the command a stage runs without being it.
var stagePrefixes = setOf("!", "{", "}", "if", "then", "else", "elif", "while", "until", "do",
	"time", "sudo", "command", "exec", "nohup", "env")

var (
	assignment  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	gitGrep     = regexp.MustCompile(`(?i)\bgit\s+grep\b`)
	grepWord    = regexp.MustCompile(`(?i)\bgrep\b`)
	findName    = regexp.MustCompile(`-name\b`)
	redirect    = regexp.MustCompile(`>>?\s*(\S*)`)
	redirectArg = regexp.MustCompile(`^\d*>`)
	sedQuiet    = regexp.MustCompile(`^-[A-Za-z]*n[A-Za-z]*$`)
	sedInPlace  = regexp.MustCompile(`^-[A-Za-z]*i`)
)

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// pipelines returns each list's stages, each stage as its words starting at
// the command it runs. Empty stages are dropped.
func pipelines(masked string) [][][]string {
	// `>&`, `&>` and `|&` are redirections, not list separators.
	s := strings.NewReplacer(">&", ">@", "&>", "@>", "|&", "|").Replace(masked)
	var lists [][][]string
	var stages [][]string
	var cur strings.Builder
	endStage := func() {
		if words := commandWords(cur.String()); len(words) > 0 {
			stages = append(stages, words)
		}
		cur.Reset()
	}
	endList := func() {
		endStage()
		if len(stages) > 0 {
			lists = append(lists, stages)
		}
		stages = nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			cur.WriteString(s[i:min(i+2, len(s))])
			i++
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			endList()
			i++
		case c == '|':
			endStage()
		case strings.IndexByte(";&()`\n", c) >= 0:
			endList()
		default:
			cur.WriteByte(c)
		}
	}
	endList()
	return lists
}

func commandWords(stage string) []string {
	words := strings.Fields(stage)
	for len(words) > 0 && (stagePrefixes[words[0]] || assignment.MatchString(words[0])) {
		words = words[1:]
	}
	if len(words) > 0 {
		name := words[0]
		if i := strings.LastIndexAny(name, `/\`); i >= 0 {
			name = name[i+1:]
		}
		words[0] = strings.ToLower(name)
	}
	return words
}

func readsRepo(head []string) bool {
	return contentSources[head[0]] || (head[0] == "git" && len(head) > 1 && gitContent[head[1]])
}

func isSearchStage(words []string, index int, head []string) bool {
	name, text := words[0], strings.Join(words, " ")
	switch {
	case searchTools[name]:
		return index == 0 || readsRepo(head)
	case gitGrep.MatchString(text):
		return true
	case name == "xargs":
		return grepWord.MatchString(text)
	case name == "find":
		return findName.MatchString(text)
	}
	return false
}

func hasSearch(masked string) bool {
	for _, stages := range pipelines(masked) {
		for i, words := range stages {
			if isSearchStage(words, i, stages[0]) {
				return true
			}
		}
	}
	return false
}

// writesFile reports a redirect into a file (not /dev/null, not a
// descriptor duplicate).
func writesFile(words []string) bool {
	for _, m := range redirect.FindAllStringSubmatch(strings.Join(words, " "), -1) {
		if m[1] != "/dev/null" && !strings.HasPrefix(m[1], "@") {
			return true
		}
	}
	return false
}

func isReadHead(words []string) bool {
	if writesFile(words) {
		return false
	}
	operands := 0
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "<<") {
			return false
		}
		if !strings.ContainsAny(w[:1], "-<>@") && !redirectArg.MatchString(w) {
			operands++
		}
	}
	if readers[words[0]] {
		return operands > 0
	}
	if words[0] == "sed" {
		quiet, inPlace := false, false
		for _, f := range words[1:] {
			if !strings.HasPrefix(f, "-") {
				continue
			}
			quiet = quiet || sedQuiet.MatchString(f)
			inPlace = inPlace || sedInPlace.MatchString(f) || strings.HasPrefix(f, "--in-place")
		}
		return quiet && !inPlace
	}
	return false
}

func hasRead(masked string) bool {
	for _, stages := range pipelines(masked) {
		if isReadHead(stages[0]) {
			return true
		}
	}
	return false
}
