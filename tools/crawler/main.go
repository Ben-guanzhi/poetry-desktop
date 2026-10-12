package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"

	"github.com/longbridgeapp/opencc"
)

const (
	apiEndpoint  = "https://zh.wikisource.org/w/api.php"
	restEndpoint = "https://zh.wikisource.org/api/rest_v1/page/html/"
)

const userAgent = "PoetryDesktopCrawler/1.0 (classic Chinese texts; personal dev tool)"

type Chapter struct {
	Chapter    string   `json:"chapter"`
	Paragraphs []string `json:"paragraphs"`
}

type Book struct {
	Name     string    `json:"name"`
	Chapters []Chapter `json:"chapters"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Fallback simplified-Chinese converter. Wikisource's own conversion (via
// Accept-Language) skips text explicitly tagged zh-Hant on a page; paragraphs
// that still look traditional after that get re-run through OpenCC locally.
var openccT2S, openccErr = opencc.New("t2s")

var (
	reHTMLTag  = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	reSpaces   = regexp.MustCompile(`\s+`)
	reTitleSeg = regexp.MustCompile(`[^/]+$`)
	// Unresolved template parameters that leak into rendered text, e.g. {{{2}}}
	reParam = regexp.MustCompile(`\{\{\{[^{}]*\}\}\}`)
	// Broken templates rendered as literal "{{*|…}" in the HTML
	reBraceRemnant = regexp.MustCompile(`\{\{[^{}]*\}`)
	// Literal wiki links that escaped parsing, e.g. [[./卷094上|匈奴传]]
	reWikiLinkText = regexp.MustCompile(`\[\[(?:[^\]|]*\|)?([^\]]*)\]\]`)
	// Page-title segments like "卷001"/"01"/"卷十三" that carry no chapter meaning
	reNumericName = regexp.MustCompile(`^(卷)?[0-9０-９一二三四五六七八九十百零〇两]{1,8}(卷)?$`)
	// 《》 decorations around index link display text
	reDecorations = regexp.MustCompile(`^《+|》+$`)
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: crawler <书名> [输出文件]")
		fmt.Println("Examples:")
		fmt.Println("  crawler 尚书")
		fmt.Println("  crawler 礼记")
		fmt.Println("  crawler 周易")
		fmt.Println("  crawler 春秋")
		os.Exit(1)
	}

	bookName := os.Args[1]
	outputFile := bookName + ".json"
	if len(os.Args) > 2 {
		outputFile = os.Args[2]
	}

	tradName := toTraditional(bookName)
	fmt.Printf("Crawling %s from wikisource (simplified output)...\n", tradName)

	book, err := crawlBook(bookName, tradName, outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Crawled %d chapters\n", len(book.Chapters))

	if err := saveBook(outputFile, book); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}

	// Crawl finished cleanly — the resume checkpoint is no longer needed.
	os.Remove(progressFile(outputFile))

	fmt.Printf("Saved to %s\n", outputFile)
	fmt.Println("提示: 将该文件放入客户端数据目录的 \"维基文库\" 文件夹，即可在客户端中查看。")
}

// crawlDelay is the pause between requests; it grows after rate-limit hits
// and stays elevated for the rest of the run.
var crawlDelay = 2 * time.Second

func crawlBook(bookName, tradName, outputFile string) (*Book, error) {
	// Chapter list: prefer the subpage links on the index page, which preserve
	// the book's own ordering (e.g. the 64 hexagrams of 周易). Fall back to an
	// alphabetical allpages scan, and append any children the index missed.
	indexPages, indexParas, indexHints := parseIndexPage(tradName)
	pages := indexPages
	if len(pages) > 0 {
		fmt.Printf("Found %d chapter links on the index page\n", len(pages))
	} else {
		fmt.Println("No chapter links on the index page, falling back to allpages search...")
	}

	all, err := listPages(tradName)
	if err != nil {
		if len(pages) == 0 {
			return nil, fmt.Errorf("failed to list pages: %v", err)
		}
		fmt.Printf("  Warning: allpages search failed: %v\n", err)
	}
	seen := make(map[string]bool, len(pages))
	for _, p := range pages {
		seen[p] = true
	}
	for _, p := range all {
		if !seen[p] {
			seen[p] = true
			pages = append(pages, p)
		}
	}
	fmt.Printf("Found %d pages in total\n", len(pages))

	// Drop "全覽" compilation pages — they duplicate every chapter in one page.
	kept := pages[:0]
	for _, p := range pages {
		if seg := reTitleSeg.FindString(p); seg == "全覽" || seg == "全览" {
			continue
		}
		kept = append(kept, p)
	}
	pages = kept

	// Resume support: chapters already crawled (recorded in the sidecar
	// progress file) are reused instead of re-fetched.
	done := loadProgress(outputFile, tradName)
	idxKey := "INDEX:" + tradName
	idxChars := indexBodyChars(indexParas)

	// Walk pages in document order and look each one up in done, so resumed
	// chapters keep the book's own order. range-ing the map directly used to
	// emit them in Go's randomized map order, leaving the finished book as
	// [shuffled old chapters] + [ordered new chapters].
	placed := make(map[string]bool, len(pages))
	chapters := make([]Chapter, 0, len(pages)+1)
	for _, page := range pages {
		if e, ok := done[page]; ok {
			chapters = append(chapters, e.Chapter)
			placed[page] = true
		}
	}
	// Chapters whose page is no longer listed (renamed upstream, or filtered out
	// as 全覽) would otherwise vanish from the book. Keep them, sorted by key so
	// the order is deterministic. The synthetic INDEX: entry is placed
	// explicitly below when it qualifies.
	var strays []string
	for key := range done {
		if !placed[key] && !(key == idxKey && idxChars >= 500) {
			strays = append(strays, key)
		}
	}
	sort.Strings(strays)
	for _, key := range strays {
		chapters = append(chapters, done[key].Chapter)
	}
	fmt.Printf("Resuming: %d chapters already crawled\n", len(chapters))

	book := &Book{Name: bookName, Chapters: chapters}

	// Some books keep their body text on the index page itself (尔雅), with
	// subpages only for editions/annotations. If the index body is substantial
	// (>= 500 chars, far above the ~400 max seen for true index pages), it
	// becomes the first chapter under the book's own name.
	if idxChars >= 500 {
		fmt.Printf("Index page holds the book's body text (%d chars)\n", idxChars)
		if e, ok := done[idxKey]; ok {
			// keep position: re-insert at front
			book.Chapters = append([]Chapter{e.Chapter}, book.Chapters...)
		} else {
			ch := Chapter{Chapter: bookName, Paragraphs: indexParas}
			book.Chapters = append([]Chapter{ch}, book.Chapters...)
			done[idxKey] = progressEntry{Page: idxKey, Chapter: ch}
			saveProgress(outputFile, tradName, done)
			saveBook(outputFile, book)
		}
	}

	for i, page := range pages {
		if _, ok := done[page]; ok {
			continue
		}
		fmt.Printf("  Fetching %d/%d: %s\n", i+1, len(pages), page)

		chapter, err := fetchChapter(bookName, page, indexHints[page])
		if err != nil {
			fmt.Printf("    Warning: %v\n", err)
			continue
		}

		if chapter != nil {
			book.Chapters = append(book.Chapters, *chapter)
			done[page] = progressEntry{Page: page, Chapter: *chapter}
			// Persist after every chapter so an interrupted run only loses
			// the chapter in flight.
			saveProgress(outputFile, tradName, done)
			saveBook(outputFile, book)
		}
		time.Sleep(crawlDelay) // Be polite to avoid rate limiting
	}

	return book, nil
}

// parseIndexPage fetches the rendered index page and extracts (a) chapter
// links in document order, so chapters keep the order defined by the book
// itself, and (b) the index page's own body paragraphs (some books like 尔雅
// keep their text on the index page).
//
// Two link tiers:
//  1. subpage links "书名/章" (e.g. 周易/乾, often inside content wikitables) —
//     the common case;
//  2. if the book has no subpages, links inside list items of the body (the
//     春秋 index lists 春秋經, 春秋左氏傳, ... as sibling titles in a <ul>).
//
// Navigation chrome (header templates, navboxes, catlinks) is excluded by
// element class, red links ("?action=edit&redlink=1") are dropped, so sibling
// books and meta pages (詩經, 周易正義, 四库全书, ...) are never picked up.
func parseIndexPage(tradName string) ([]string, []string, map[string]string) {
	htmlStr, err := fetchRESTHTML(tradName)
	if err != nil {
		fmt.Printf("  Warning: failed to fetch index page: %v\n", err)
		return nil, nil, nil
	}

	root, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		fmt.Printf("  Warning: failed to parse index page: %v\n", err)
		return nil, nil, nil
	}

	var children, siblings []string
	seenChild, seenSibling := make(map[string]bool), make(map[string]bool)
	// linkText holds each chapter link's display text (already zh-hans
	// converted) — a better chapter name than "卷001"-style page titles.
	linkText := make(map[string]string)

	// Collect <li> elements outside navigation chrome for the sibling tier.
	var listItems []*html.Node
	walkHTML(root, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		if hasSkipClass(n) {
			return false
		}
		switch n.Data {
		case "style", "script", "head", "title":
			return false
		case "li":
			listItems = append(listItems, n)
		}
		return true
	})

	classify := func(n *html.Node, requirePrefix bool) {
		href := attrValue(n, "href")
		href = html.UnescapeString(strings.TrimPrefix(href, "./"))
		if href == "" || strings.HasPrefix(href, "#") ||
			strings.Contains(href, ":") || strings.Contains(href, "?") ||
			href == tradName {
			return
		}
		if decoded, err := url.PathUnescape(href); err == nil {
			href = decoded
		}
		isChild := strings.HasPrefix(href, tradName+"/")
		if requirePrefix && !isChild {
			return
		}
		name := strings.TrimSpace(reDecorations.ReplaceAllString(strings.Join(blockText(n), " "), ""))
		if isChild {
			if !seenChild[href] {
				seenChild[href] = true
				children = append(children, href)
				linkText[href] = name
			}
		} else if !seenSibling[href] {
			seenSibling[href] = true
			siblings = append(siblings, href)
			linkText[href] = name
		}
	}

	// Tier 1: subpage links anywhere in content (table cells included).
	walkHTML(root, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		if hasSkipClass(n) {
			return false
		}
		switch n.Data {
		case "style", "script", "head", "title", "figure", "sup":
			return false
		case "a":
			classify(n, true)
		}
		return true
	})
	indexParas := extractParagraphs(root)
	if len(children) > 0 {
		return children, indexParas, linkText
	}

	// Tier 2: sibling-title chapter links inside body list items. Links
	// elsewhere (e.g. inside the page-header template) are ignored.
	for _, li := range listItems {
		walkHTML(li, func(n *html.Node) bool {
			if n.Type == html.ElementNode && n.Data == "a" {
				classify(n, false)
			}
			return true
		})
	}
	return siblings, indexParas, linkText
}

// indexBodyChars counts the total characters of the index page's body text,
// used to decide whether the book's content lives on the index page itself.
func indexBodyChars(paras []string) int {
	n := 0
	for _, p := range paras {
		n += len([]rune(p))
	}
	return n
}

// listPages returns all child pages of prefix via list=allpages. It is limited
// to namespace 0 and non-redirects, and filters out same-prefix siblings such
// as 周易正義 when crawling 周易.
func listPages(prefix string) ([]string, error) {
	var pages []string
	continueToken := ""

	for {
		v := url.Values{}
		v.Set("action", "query")
		v.Set("list", "allpages")
		v.Set("apprefix", prefix)
		v.Set("apnamespace", "0")
		v.Set("apfilterredir", "nonredirects")
		v.Set("aplimit", "max")
		v.Set("format", "json")
		v.Set("formatversion", "2")
		if continueToken != "" {
			v.Set("apcontinue", continueToken)
		}

		resp, err := apiGet(apiEndpoint+"?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}

		var apiResp struct {
			Error *apiError `json:"error"`
			Query struct {
				AllPages []struct {
					Title string `json:"title"`
				} `json:"allpages"`
			} `json:"query"`
			Continue struct {
				APContinue string `json:"apcontinue"`
			} `json:"continue"`
		}

		if err := json.Unmarshal(resp, &apiResp); err != nil {
			return nil, err
		}
		if apiResp.Error != nil {
			return nil, apiResp.Error
		}

		for _, p := range apiResp.Query.AllPages {
			t := p.Title
			if t != prefix && strings.HasPrefix(t, prefix+"/") {
				pages = append(pages, t)
			}
		}

		if apiResp.Continue.APContinue == "" {
			break
		}
		continueToken = apiResp.Continue.APContinue
	}

	return pages, nil
}

// fetchChapter fetches one page's rendered content (already converted to
// simplified Chinese) and its simplified display title.
func fetchChapter(bookName, pageName, nameHint string) (*Chapter, error) {
	htmlStr, err := fetchRESTHTML(pageName)
	if err != nil {
		return nil, err
	}

	root, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil, err
	}
	paragraphs := extractParagraphs(root)
	if len(paragraphs) == 0 {
		return nil, fmt.Errorf("no paragraphs found")
	}
	for i, p := range paragraphs {
		paragraphs[i] = simplifyIfTraditional(p, 1)
	}

	chapterName := reTitleSeg.FindString(pageName)
	if display, err := fetchDisplayTitle(pageName); err == nil && display != "" {
		chapterName = simplifyIfTraditional(simplifyChapterName(bookName, display), 1)
	}
	// "卷001"-style page titles make poor chapter names; prefer the index
	// link's display text (e.g. 五帝本纪第一), which is already zh-hans.
	// Hints must look like chapter titles: contain a structural keyword and
	// be short, punctuation-free — quote excerpts on some indexes are rejected.
	if nameHint != "" && reNumericName.MatchString(chapterName) {
		if len([]rune(nameHint)) <= 16 && !strings.ContainsAny(nameHint, "。，；？！、：“”") &&
			strings.ContainsAny(nameHint, "篇传纪志卷第表序论书赞本典谏说章疏议对") {
			chapterName = simplifyIfTraditional(nameHint, 1)
		}
	}

	return &Chapter{
		Chapter:    chapterName,
		Paragraphs: paragraphs,
	}, nil
}

// simplifyChapterName turns a converted full title like "诗经/国风/周南" into
// a chapter name relative to the book ("国风·周南").
func simplifyChapterName(bookName, display string) string {
	if prefix := bookName + "/"; strings.HasPrefix(display, prefix) {
		return strings.ReplaceAll(display[len(prefix):], "/", "·")
	}
	if i := strings.LastIndexByte(display, '/'); i >= 0 {
		return display[i+1:]
	}
	return display
}

// simplifyIfTraditional runs OpenCC t2s on s and adopts the result only if it
// differs by at least minDiffs CJK characters — i.e. the text still contains
// traditional/variant forms that Wikisource's conversion left behind. For
// already-converted text t2s is (near) identity, so well-formed paragraphs
// are returned unchanged.
// protectChars are valid simplified characters that a naive t2s pass would
// wrongly alter (e.g. the music note 徵 in 宫商角徵羽); if a paragraph's only
// differences involve these, it is left alone.
var protectChars = map[rune]bool{'徵': true, '乾': true}

// variantNormalize maps exotic variant characters that OpenCC's dictionary
// misses but which never occur in simplified text.
var variantNormalize = strings.NewReplacer("偸", "偷", "墬", "地")

func simplifyIfTraditional(s string, minDiffs int) string {
	if openccErr != nil || s == "" {
		return s
	}
	s = variantNormalize.Replace(s)
	conv, err := openccT2S.Convert(s)
	if err != nil || conv == s {
		return s
	}
	a, b := []rune(s), []rune(conv)
	if len(a) != len(b) {
		// phrase-level resegmentation implies real conversion work
		return conv
	}
	out := make([]rune, len(a))
	diffs := 0
	for i := range a {
		switch {
		case a[i] == b[i]:
			out[i] = a[i]
		case protectChars[a[i]]:
			out[i] = a[i] // keep, does not count as a difference
		default:
			if isCJK(a[i]) {
				diffs++
			}
			out[i] = b[i]
		}
	}
	if diffs >= minDiffs {
		return string(out)
	}
	return s
}

func isCJK(r rune) bool {
	return (r >= 0x3400 && r <= 0x9FFF) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0x20000 && r <= 0x2FA1F)
}

// fetchRESTHTML fetches the rendered Parsoid HTML of a page, converted to
// simplified Chinese via the Accept-Language header (Wikisource's own
// word-aware conversion tables).
func fetchRESTHTML(title string) (string, error) {
	reqURL := restEndpoint + url.PathEscape(title)
	data, err := apiGet(reqURL, map[string]string{"Accept-Language": "zh-hans"})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// fetchDisplayTitle returns the simplified display title of a page via
// action=parse&prop=displaytitle&variant=zh-hans, e.g. "周易/系辞上".
func fetchDisplayTitle(page string) (string, error) {
	v := url.Values{}
	v.Set("action", "parse")
	v.Set("page", page)
	v.Set("prop", "displaytitle")
	v.Set("variant", "zh-hans")
	v.Set("format", "json")
	v.Set("formatversion", "2")

	resp, err := apiGet(apiEndpoint+"?"+v.Encode(), nil)
	if err != nil {
		return "", err
	}

	var apiResp struct {
		Error *apiError `json:"error"`
		Parse struct {
			DisplayTitle string `json:"displaytitle"`
		} `json:"parse"`
	}
	if err := json.Unmarshal(resp, &apiResp); err != nil {
		return "", err
	}
	if apiResp.Error != nil {
		return "", apiResp.Error
	}

	title := reHTMLTag.ReplaceAllString(apiResp.Parse.DisplayTitle, "")
	title = html.UnescapeString(strings.TrimSpace(title))
	return reTitleSeg.FindString(title), nil
}

type apiError struct {
	Code string `json:"code"`
	Info string `json:"info"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("API error %s: %s", e.Code, e.Info)
}

// ---------- HTML content extraction ----------

// skipTags are elements whose whole subtree carries no body text: layout
// tables (page header/navbox templates), styles, images, reference markers
// and headings.
var skipTags = map[string]bool{
	"table": true, "style": true, "script": true, "head": true, "title": true,
	"figure": true, "figcaption": true, "sup": true, "nav": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// blockTags are elements rendered as standalone text blocks; each becomes one
// paragraph. Nested lists inside a list item are still visited separately.
var blockTags = map[string]bool{
	"p": true, "li": true, "dd": true, "dt": true,
}

// skipClasses mark rendered "meta" content that would otherwise produce
// paragraphs (reference lists, notes, navigation boxes).
var skipClasses = []string{
	"references", "reflist", "hatnote", "navbox", "noprint", "sidebar",
	"headertemplate", "catlinks", "printfooter", "mw-navigation",
	"metadata", "textquality", "license",
}

func extractParagraphs(root *html.Node) []string {
	var paragraphs []string
	walkHTML(root, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		if skipTags[n.Data] || hasSkipClass(n) {
			return false // skip subtree
		}
		if blockTags[n.Data] {
			for _, text := range blockText(n) {
				if hasVisibleText(text) {
					paragraphs = append(paragraphs, text)
				}
			}
			return true // keep walking for nested lists inside the item
		}
		return true
	})
	return paragraphs
}

// walkHTML does a depth-first walk; the callback's return value decides
// whether to descend into the node's children.
func walkHTML(n *html.Node, visit func(n *html.Node) bool) {
	if !visit(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkHTML(c, visit)
	}
}

func hasSkipClass(n *html.Node) bool {
	cls := strings.ToLower(attrValue(n, "class"))
	if cls == "" {
		return false
	}
	for _, c := range skipClasses {
		if strings.Contains(cls, c) {
			return true
		}
	}
	return false
}

// blockText collects the visible text of a block element as one or more
// lines. Nested lists/tables inside the block are excluded (they are visited
// separately); <br> starts a new line.
func blockText(n *html.Node) []string {
	var sb strings.Builder
	var collect func(x *html.Node)
	collect = func(x *html.Node) {
		if x.Type == html.TextNode {
			sb.WriteString(x.Data)
			return
		}
		if x.Type == html.ElementNode {
			if x.Data == "br" {
				sb.WriteString("\n")
				return
			}
			if skipTags[x.Data] || x.Data == "ul" || x.Data == "ol" || x.Data == "dl" || hasSkipClass(x) {
				return
			}
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)

	var lines []string
	for _, line := range strings.Split(sb.String(), "\n") {
		line = reParam.ReplaceAllString(line, "")
		line = reBraceRemnant.ReplaceAllString(line, "")
		line = reWikiLinkText.ReplaceAllString(line, "$1")
		line = reSpaces.ReplaceAllString(strings.TrimSpace(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasVisibleText(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// ---------- Incremental save & resume ----------

type progressEntry struct {
	Page    string  `json:"page"`
	Chapter Chapter `json:"chapter"`
}

func progressFile(outputFile string) string {
	return outputFile + ".progress.json"
}

// saveBook writes the output atomically (temp file + rename), safe to call
// after every chapter.
func saveBook(outputFile string, book *Book) error {
	data, err := json.MarshalIndent(book, "", "  ")
	if err != nil {
		return err
	}
	tmp := outputFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, outputFile)
}

func saveProgress(outputFile, tradName string, done map[string]progressEntry) error {
	entries := make([]progressEntry, 0, len(done))
	for _, e := range done {
		entries = append(entries, e)
	}
	data, err := json.MarshalIndent(struct {
		Book    string          `json:"book"`
		Entries []progressEntry `json:"entries"`
	}{tradName, entries}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(progressFile(outputFile), data, 0644)
}

// loadProgress returns previously crawled chapters keyed by page title. The
// checkpoint only applies to the same book; a mismatched or broken file is
// ignored so the run starts fresh.
func loadProgress(outputFile, tradName string) map[string]progressEntry {
	data, err := os.ReadFile(progressFile(outputFile))
	if err != nil {
		return map[string]progressEntry{}
	}
	var pf struct {
		Book    string          `json:"book"`
		Entries []progressEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &pf); err != nil || pf.Book != tradName {
		return map[string]progressEntry{}
	}
	done := make(map[string]progressEntry, len(pf.Entries))
	for _, e := range pf.Entries {
		if e.Page != "" && e.Chapter.Chapter != "" {
			done[e.Page] = e
		}
	}
	return done
}

// ---------- HTTP ----------

func apiGet(reqURL string, headers map[string]string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}

		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == 429 {
			wait := 30 + attempt*30
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if n, err := strconv.Atoi(ra); err == nil && n > 0 {
					wait = n
				}
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			fmt.Printf("    Rate limited, waiting %d seconds...\n", wait)
			time.Sleep(time.Duration(wait) * time.Second)
			// Back off for the rest of the run so we stop triggering 429s.
			if crawlDelay < 15*time.Second {
				crawlDelay += 2 * time.Second
				fmt.Printf("    Raising inter-request delay to %v\n", crawlDelay)
			}
			lastErr = fmt.Errorf("HTTP 429")
			continue
		}

		if resp.StatusCode != 200 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}

	return nil, fmt.Errorf("failed after retries: %v", lastErr)
}

func toTraditional(name string) string {
	mapping := map[string]string{
		"尚书":   "尚書",
		"礼记":   "禮記",
		"周易":   "周易",
		"易经":   "周易",
		"春秋":   "春秋",
		"诗经":   "詩經",
		"论语":   "論語",
		"孟子":   "孟子",
		"大学":   "大學",
		"中庸":   "中庸",
		"老子":   "老子",
		"庄子":   "莊子",
		"荀子":   "荀子",
		"左传":   "春秋左氏傳",
		"周礼":   "周禮",
		"仪礼":   "儀禮",
		"孝经":   "孝經",
		"尔雅":   "爾雅",
		"世说新语": "世說新語",
		"史记":   "史記",
		"汉书":   "漢書",
		"后汉书":  "後漢書",
		"三国志":  "三國志",
		"论衡":   "論衡",
		"说苑":   "說苑",
		"盐铁论":  "鹽鐵論",
		"抱朴子":  "抱樸子",
		"贞观政要": "貞觀政要",
		"潜夫论":  "潛夫論",
		"新语":   "新語",
		"水经注":  "水經注",
		"商君书":  "商君書",
		"韩诗外传": "韓詩外傳",
		"文心雕龙": "文心雕龍",
		"风俗通义": "風俗通義",
		"申鉴":   "申鑒",
		"六韬":   "六韜",
		"中论":   "中論",
	}
	if trad, ok := mapping[name]; ok {
		return trad
	}
	return name
}
