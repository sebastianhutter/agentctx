package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/sebastianhutter/agentctx/internal/store"
)

// md renders the vault's markdown. No raw HTML: note bodies are written by
// agents, and a page here should render what they wrote rather than execute it.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// decorated is an entry with what the view needs to show about it.
type decorated struct {
	*store.Entry
	Stale       bool
	StaleReason string
	Body        template.HTML
	Age         string
}

// decorate applies the same staleness rules an agent gets, so the page and the
// bundle never disagree about whether something needs re-checking.
func decorate(entries []*store.Entry, maxAgeDays int) []decorated {
	out := make([]decorated, 0, len(entries))
	for _, e := range entries {
		d := decorated{Entry: e, Body: markdown(e.Body)}

		if ts, err := store.ParseTime(e.TS); err == nil {
			d.Age = humanAge(time.Since(ts))
		}

		switch {
		case e.Source != nil && e.Source.Kind == store.SourceInferred:
			d.Stale, d.StaleReason = true, "inferred — not evidence"
		case e.Source != nil && e.Source.Retrieved != "":
			retrieved, err := store.ParseTime(e.Source.Retrieved)
			if err != nil {
				d.Stale, d.StaleReason = true, "unparseable retrieval date"
				break
			}
			if age := int(time.Since(retrieved).Hours() / 24); age > maxAgeDays {
				d.Stale, d.StaleReason = true, fmt.Sprintf("retrieved %d days ago", age)
			}
		}
		out = append(out, d)
	}
	return out
}

// wikilink matches Obsidian's [[task-id]] syntax.
var wikilink = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|([^\[\]]+))?\]\]`)

// linkWikilinks turns [[proj-210]] into a link to that task.
//
// The vault's own syntax, which Obsidian resolves by filename and markdown does
// not know at all — left alone it renders as literal brackets, which is how a
// dependency entry looked in the first version of this page. Resolution goes
// through /t/, since a note body does not always carry its project.
func linkWikilinks(source string) string {
	return wikilink.ReplaceAllStringFunc(source, func(match string) string {
		parts := wikilink.FindStringSubmatch(match)
		target := strings.TrimSpace(parts[1])
		label := target
		if parts[2] != "" {
			label = strings.TrimSpace(parts[2])
		}
		return fmt.Sprintf("[%s](/t/%s)", label, url.PathEscape(target))
	})
}

// hasContent reports whether a note body says anything, treating a heading with
// nothing under it as saying nothing.
//
// A freshly created note carries the section skeleton — Current understanding,
// Decisions, Findings, Open threads — so a plain emptiness test calls it
// written and the page renders four empty headings instead of admitting it has
// never been consolidated.
func hasContent(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return true
	}
	return false
}

// markdown renders a fragment of the vault to HTML.
func markdown(source string) template.HTML {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(linkWikilinks(source)), &buf); err != nil {
		// Show the source rather than nothing: an unrenderable note is still
		// readable, and losing it to a parser error would be worse.
		return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
	}
	return template.HTML(buf.String())
}

// humanAge renders a duration the way someone scanning a log wants to read it.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"join":     strings.Join,
		"markdown": markdown,
		"firstLine": func(s string) string {
			s = strings.TrimSpace(s)
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[:i]
			}
			if len(s) > 160 {
				s = s[:160] + "…"
			}
			return s
		},
		"kindClass": func(kind string) string { return "kind-" + kind },
		// Builds a sub-template's argument, since a template call takes one.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict needs key/value pairs, got %d arguments", len(kv))
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		// Takes an explicit plural for the words English declines to make
		// regular — "entry" is the one this vault says constantly.
		"plural": func(n int, singular string, plural ...string) string {
			if n == 1 {
				return fmt.Sprintf("%d %s", n, singular)
			}
			if len(plural) > 0 {
				return fmt.Sprintf("%d %s", n, plural[0])
			}
			return fmt.Sprintf("%d %ss", n, singular)
		},
	}
}
