package rss

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"
)

// node is a minimal XML tree. Text runs are kept as kids with isText set,
// so mixed content (Atom xhtml) can be turned back into HTML.
type node struct {
	name   xml.Name
	attr   []xml.Attr
	kids   []*node
	text   string
	isText bool
}

const (
	maxDepth = 64
	maxNodes = 500_000
)

// parseXML builds a tree from a whole document. It fails on malformed,
// truncated or empty input.
func parseXML(b []byte) (*node, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	d.Entity = xml.HTMLEntity // &nbsp; and friends show up in real feeds
	d.CharsetReader = charsetReader
	var (
		root  *node
		stack []*node
		count int
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if root != nil && len(stack) == 0 {
				return nil, errors.New("invalid XML: more than one root element")
			}
			if count++; count > maxNodes || len(stack) >= maxDepth {
				return nil, errors.New("XML document too large or too deeply nested")
			}
			n := &node{name: t.Name, attr: t.Attr}
			if len(stack) == 0 {
				root = n
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			p := stack[len(stack)-1]
			if k := len(p.kids); k > 0 && p.kids[k-1].isText {
				p.kids[k-1].text += string(t)
			} else {
				p.kids = append(p.kids, &node{isText: true, text: string(t)})
			}
		}
	}
	if root == nil {
		return nil, errors.New("invalid XML: no root element")
	}
	if len(stack) != 0 {
		return nil, errors.New("invalid XML: unexpected end of document")
	}
	return root, nil
}

func (n *node) is(space, local string) bool {
	return n != nil && !n.isText && n.name.Space == space && n.name.Local == local
}

// first returns the first child element with the given namespace and name.
func (n *node) first(space, local string) *node {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.is(space, local) {
			return k
		}
	}
	return nil
}

// all returns the child elements with the given namespace and name.
func (n *node) all(space, local string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, k := range n.kids {
		if k.is(space, local) {
			out = append(out, k)
		}
	}
	return out
}

// content returns the element's direct text (CDATA included), or "".
func (n *node) content() string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for _, k := range n.kids {
		if k.isText {
			b.WriteString(k.text)
		}
	}
	return b.String()
}

// value is content, trimmed.
func (n *node) value() string { return strings.TrimSpace(n.content()) }

// get returns the value of an unqualified attribute.
func (n *node) get(local string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.attr {
		if a.Name.Local == local && (a.Name.Space == "" || a.Name.Space == n.name.Space) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// innerHTML serializes the element's children back into HTML (for Atom
// type="xhtml" constructs).
func (n *node) innerHTML() string {
	var b strings.Builder
	var walk func(*node)
	walk = func(m *node) {
		for _, k := range m.kids {
			if k.isText {
				b.WriteString(html.EscapeString(k.text))
				continue
			}
			b.WriteString("<" + k.name.Local)
			for _, a := range k.attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				fmt.Fprintf(&b, ` %s="%s"`, a.Name.Local, html.EscapeString(a.Value))
			}
			b.WriteString(">")
			walk(k)
			b.WriteString("</" + k.name.Local + ">")
		}
	}
	if n != nil {
		walk(n)
	}
	return b.String()
}

// charsetReader supports the non-UTF-8 encodings that feeds still use.
func charsetReader(label string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return r, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1":
		return decode8bit(r, nil)
	case "windows-1252", "cp1252":
		return decode8bit(r, &cp1252)
	}
	return nil, fmt.Errorf("unsupported charset %q", label)
}

func decode8bit(r io.Reader, high *[32]rune) (io.Reader, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	out.Grow(len(b))
	for _, c := range b {
		switch {
		case c >= 0x80 && c < 0xa0 && high != nil && high[c-0x80] != 0:
			out.WriteRune(high[c-0x80])
		default:
			out.WriteRune(rune(c))
		}
	}
	return strings.NewReader(out.String()), nil
}

// cp1252 maps bytes 0x80-0x9F of windows-1252 (0 = undefined).
var cp1252 = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}
