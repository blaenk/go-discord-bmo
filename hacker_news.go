package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// import "text/template"
import "time"

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

func (v *Visitor) CollectedText() string {
	return v.buffer.String()
}

// Item is a representation of a Hacker News story or comment
type Item struct {
	Author      string `json:"by"`
	Body        string `json:"text"`
	ID          int    `json:"id"`
	Parent      int    `json:"parent"`
	Descendants int    `json:"descendants"`
	Kids        []int  `json:"kids"`
	Score       int    `json:"score"`
	Time        int64  `json:"time"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	URL         string `json:"url"`
}

func getHNItem(id int) *Item {
	url := fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%d.json", id)

	resp, err := http.Get(url)

	if err != nil {
		log.Panicln("Couldn't get a response:", err)
	}

	decoder := json.NewDecoder(resp.Body)

	item := &Item{}

	if err = decoder.Decode(&item); err != nil {
		log.Panicln("Decoding err:", err)
	}

	return item
}

func (i *Item) formatTime() string {
	pst, err := time.LoadLocation("America/Los_Angeles")

	if err != nil {
		log.Panicln("Can't load America/Los_Angeles")
	}

	t := time.Unix(i.Time, 0).In(pst)

	return t.Format("3:04pm PST on Monday, January 2")
}

func (i *Item) formatStory() string {
	if i.Type != "story" {
		log.Panicln("Attempting to format an item that isn't a story")
	}

	icon := "<:ycombinator:239206737075240960>"
	date := i.formatTime()

	sprintf := fmt.Sprintf(`%s **%s**
**%d** points. **%d** comments. posted at %s

thread: %s
target: %s`,
		icon,
		i.Title,
		i.Score,
		i.Descendants,
		date,
		i.itemURL(),
		i.URL)

	return sprintf
}

func (i *Item) formatCommentBody() string {
	doc, err := html.Parse(strings.NewReader(i.Body))

	if err != nil {
		log.Panicln("Coudln't parse HTML:", err)
	}

	v := &Visitor{}

	v.Visit(doc)

	return v.CollectedText()
}

func (i *Item) itemURL() string {
	return fmt.Sprintf("https://news.ycombinator.com/item?id=%d", i.ID)
}

func (i *Item) authorURL() string {
	return fmt.Sprintf("https://news.ycombinator.com/user?id=%s", i.Author)
}

func (i *Item) findRoot() *Item {
	if i == nil {
		log.Panicln("Couldn't find root")
	}

	log.Println("Looking for root of", i.ID, "with ID", i.Parent)

	parent := getHNItem(i.Parent)

	if parent == nil {
		log.Panicln("Coudln't find item", i.Parent)
	}

	log.Println("Found parent", parent.ID, "which is a", parent.Type)

	switch parent.Type {
	case "story":
		return parent

	case "comment":
		return parent.findRoot()

	default:
		return nil
	}
}

func (i *Item) formatComment() string {
	if i.Type != "comment" {
		log.Panicln("Attempting to format an item that isn't a story")
	}

	root := i.findRoot()

	if root == nil {
		log.Panicln("Couldn't find root of", i.Parent)
	}

	icon := "<:ycombinator:239206737075240960>"

	sprintf := fmt.Sprintf(`%s **%s**
comment posted by %s at %s

:speech_left: **BEGIN QUOTE** :speech_balloon:

%s

:speech_left: **END QUOTE** :speech_balloon:

thread: %s
comment: %s`,
		icon,
		root.Title,
		i.Author,
		i.formatTime(),
		i.formatCommentBody(),
		i.itemURL(),
		root.itemURL())

	return sprintf
}

func (i *Item) Format() string {
	switch i.Type {
	case "story":
		return i.formatStory()
	case "comment":
		return i.formatComment()
	default:
		log.Panicln("Unknown HN item type:", i.Type)
	}

	return "Unknown HN item type: " + i.Type
}
