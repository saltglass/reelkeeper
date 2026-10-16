package cardigann

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// parseMarkup reads an HTML page, or an XML document (some trackers
// answer in XML; their definitions use CSS selectors on it too).
func parseMarkup(page string, isXML bool) (*goquery.Document, error) {
	if !isXML {
		return goquery.NewDocumentFromReader(strings.NewReader(page))
	}
	root, err := xmlTree(page)
	if err != nil {
		return nil, err
	}
	return goquery.NewDocumentFromNode(root), nil
}

// xmlTree builds an HTML-style node tree from XML, keeping element names
// as written (an HTML parser would drop the text of <link> and friends).
func xmlTree(page string) (*html.Node, error) {
	doc := &html.Node{Type: html.DocumentNode}
	cur := doc
	d := xml.NewDecoder(strings.NewReader(page))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	for {
		// RawToken keeps namespace prefixes as written ("torznab:attr").
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &html.Node{Type: html.ElementNode, Data: elementName(t.Name)}
			for _, a := range t.Attr {
				n.Attr = append(n.Attr, html.Attribute{Key: elementName(a.Name), Val: a.Value})
			}
			cur.AppendChild(n)
			cur = n
		case xml.EndElement:
			if cur.Parent != nil {
				cur = cur.Parent
			}
		case xml.CharData:
			cur.AppendChild(&html.Node{Type: html.TextNode, Data: string(t)})
		}
	}
	return doc, nil
}

// elementName writes a name with its prefix, as selectors expect
// ("torznab:attr", selected as "torznab\:attr"), in lower case: CSS
// selectors match element names case-insensitively, so "pubDate" in a
// definition is looked up as "pubdate".
func elementName(n xml.Name) string {
	if n.Space == "" {
		return strings.ToLower(n.Local)
	}
	return strings.ToLower(n.Space + ":" + n.Local)
}
