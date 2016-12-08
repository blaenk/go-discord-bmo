package hn

import (
	"bytes"

	"golang.org/x/net/html"
)

// Visitor is a Hacker News comment body HTML Visitor
type Visitor struct {
	buffer bytes.Buffer
}

func (v *Visitor) visitText(node *html.Node) {
	v.buffer.WriteString(node.Data)
}

func (v *Visitor) visitLink(node *html.Node) {
	for _, attr := range node.Attr {
		if attr.Key == "href" {
			v.buffer.WriteString(attr.Val)
			break
		}
	}
}

func (v *Visitor) visitParagraph(node *html.Node) {
	v.buffer.WriteString("\n\n")

	v.visitChildren(node)
}

func (v *Visitor) visitCode(node *html.Node) {
	v.buffer.WriteString("```\n")

	v.visitChildren(node)

	v.buffer.WriteString("```")
}

func (v *Visitor) visitItalic(node *html.Node) {
	v.buffer.WriteString("*")

	v.visitChildren(node)

	v.buffer.WriteString("*")
}

func (v *Visitor) visitElement(node *html.Node) {
	switch node.Data {
	case "a":
		v.visitLink(node)
	case "p":
		v.visitParagraph(node)
	case "code":
		v.visitCode(node)
	case "i":
		v.visitItalic(node)
	default:
		v.visitChildren(node)
	}
}

func (v *Visitor) visitChildren(node *html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		v.Visit(child)
	}
}

// Visit traverses the HTML document structure, converting the content to
// Discord's Markdown-ish format as it goes.
func (v *Visitor) Visit(node *html.Node) {
	if node == nil {
		return
	}

	switch node.Type {
	case html.DocumentNode:
		v.visitChildren(node)
	case html.TextNode:
		v.visitText(node)
	case html.ElementNode:
		v.visitElement(node)
	}
}

// CollectedText produces the Discord Markdown-ish content.
func (v *Visitor) CollectedText() string {
	return v.buffer.String()
}
