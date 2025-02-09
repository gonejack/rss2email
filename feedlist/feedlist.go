// Package feedlist is a trivial wrapper for maintaining a list
// of RSS feeds in a file.
package feedlist

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// fetchFeed fetches a feed from the remote URL.
//
// We must use this instead of the URL handler that the feed-parser supports
// because reddit, and some other sites, will just return a HTTP error-code
// if we're using a standard "spider" User-Agent.
//
func fetchFeed(url string) (string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "rss2email (https://github.com/skx/rss2email)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	output, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func fetchFeedAndParse(url string) (*gofeed.Feed, error) {

	// Fetch the URL
	txt, err := fetchFeed(url)
	if err != nil {
		return nil, fmt.Errorf("error processing %s - %s", url, err.Error())
	}

	// Parse it
	fp := gofeed.NewParser()
	feed, err := fp.ParseString(txt)
	if err != nil {
		return nil, fmt.Errorf("error parsing %s contents: %s", url, err.Error())
	}

	return feed, nil
}

const (
	fetchMaxTries   = 5
	fetchRetryDelay = 200 * time.Millisecond
)

// Feed takes an URL as input, and returns a *gofeed.Feed.
func Feed(url string) (*gofeed.Feed, error) {
	var feed *gofeed.Feed
	var err error

	// Try up to fetchMaxTries times
	for i := 0; i < fetchMaxTries; i++ {
		// Rate limit to avoid hammering the server
		time.Sleep(time.Duration(i) * fetchRetryDelay)

		feed, err = fetchFeedAndParse(url)
		if err == nil {
			return feed, nil
		}
	}

	return nil, err
}

// expandedEntry is a url with its comment from the feeds file.
type expandedEntry struct {
	// url is the feed's url
	url string

	// comments contains the blank lines and comments preceding the url
	comments []string
}

// FeedList is the list of our feeds.
type FeedList struct {

	// filename is the name of the state-file we use
	filename string

	// expandedEntries contains an array of feed URLS.
	expandedEntries []expandedEntry
}

// New returns a new instance of the feedlist.
//
// The existing feedlist file will be read, if present, to populate the
// list of feeds.
func New(filename string) *FeedList {

	// Create the object
	m := new(FeedList)

	// If there was no path specified then create something
	// sensible.
	if filename == "" {

		// Default to using $HOME for our storage
		home := os.Getenv("HOME")

		// If that fails then get the current user, and use
		// their home if possible.
		if home == "" {
			usr, err := user.Current()
			if err == nil {
				home = usr.HomeDir
			}
		}

		// Now build up our file-path
		filename = filepath.Join(home, ".rss2email", "feeds")
	}

	// Save our updated filename
	m.filename = filename

	// Open our input-file
	file, err := os.Open(filename)
	if err == nil {
		defer file.Close()

		seenFeed := make(map[string]bool)

		//
		// Process it line by line.
		//
		comments := make([]string, 0)
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			tmp := scanner.Text()
			tmp = strings.TrimSpace(tmp)

			//
			// Save non-url lines as comments
			//
			if tmp == "" || strings.HasPrefix(tmp, "#") {
				comments = append(comments, tmp)
				continue
			}

			eEntry := expandedEntry{url: tmp, comments: comments}
			comments = make([]string, 0)

			if !seenFeed[eEntry.url] {
				m.expandedEntries = append(m.expandedEntries, eEntry)
				seenFeed[eEntry.url] = true
			}
		}
	}

	return m
}

// Entries returns the configured feeds.
func (f *FeedList) Entries() []string {
	urls := make([]string, len(f.expandedEntries))
	for i, eEntry := range f.expandedEntries {
		urls[i] = eEntry.url
	}
	return (urls)
}

// Add adds new entries to the feed-list, avoiding duplicates.
// You must call `Save` if you wish this addition to be persisted.
func (f *FeedList) Add(uris ...string) []error {

	// Maintain a map of seen entries to avoid duplicates
	seen := make(map[string]bool)

	for _, eEntry := range f.expandedEntries {
		seen[eEntry.url] = true
	}

	errors := make([]error, 0)
	for _, uri := range uris {
		if !seen[uri] {
			feed, err := Feed(uri)
			comments := []string{""}

			if err != nil {
				errors = append(errors, fmt.Errorf("%s: not added, %s", uri, err.Error()))
				continue
			}

			// By default, comments is a blank line followed by a
			// the commented feed title.
			title := feed.Title
			if title != "" {
				comments = append(comments, "# "+title)
			}

			eEntry := expandedEntry{url: uri, comments: comments}
			f.expandedEntries = append(f.expandedEntries, eEntry)
		}

		seen[uri] = true
	}

	return errors
}

// Delete removes an entry from our list of feeds.
// You must call `Save` if you wish this removal to be persisted.
func (f *FeedList) Delete(url string) {

	var tmp []expandedEntry

	for _, eEntry := range f.expandedEntries {
		if eEntry.url != url {
			tmp = append(tmp, eEntry)
		}
	}

	f.expandedEntries = tmp
}

// Save syncs our entries to disc.
func (f *FeedList) Save() error {

	// Of course we need to make sure the directory exists before
	// we can write beneath it.
	dir, _ := filepath.Split(f.filename)
	os.MkdirAll(dir, os.ModePerm)

	// Open the file
	fh, err := os.Create(f.filename)
	if err != nil {
		return fmt.Errorf("error writing to %s - %s", f.filename, err.Error())
	}

	verbose := false
	f.WriteAllEntriesIncludingComments(fh, verbose)

	fh.Close()

	return nil
}

const maxInt = int(^uint(0) >> 1)

// feedInfo returns a string containing information about a feed
func feedInfo(url string) string {
	feed, err := Feed(url)
	if err != nil {
		return ""
	}

	entriesString := "entries"
	if len(feed.Items) == 1 {
		entriesString = "entry"
	}
	info := fmt.Sprintf("%d %s", len(feed.Items), entriesString)

	oldest := -1
	newest := maxInt
	for _, item := range feed.Items {
		if item.PublishedParsed == nil {
			return info
		}

		age := int(time.Since(*item.PublishedParsed) / (24 * time.Hour))
		if age > oldest {
			oldest = age
		}

		if age < newest {
			newest = age
		}
	}

	info = fmt.Sprintf("%s, aged %d-%d days", info, newest, oldest)

	return info
}

// WriteAllEntriesIncludingComments Writes the feed list, including comments.
func (f *FeedList) WriteAllEntriesIncludingComments(writer io.Writer, verbose bool) {
	// For each entry in the list ..
	for _, eEntry := range f.expandedEntries {

		// Print the uri comments
		for _, s := range eEntry.comments {
			fmt.Fprintf(writer, "%s\n", s)
		}

		if verbose {
			info := feedInfo(eEntry.url)
			if info != "" {
				fmt.Fprintf(writer, "# %s\n", info)
			}
		}

		// Print the uri
		fmt.Fprintf(writer, "%s\n", eEntry.url)
	}
}
