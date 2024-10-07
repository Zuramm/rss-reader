package main

import (
	"database/sql"
	"log"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/mattn/go-sqlite3"
	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"
)

type PostFetcher struct {
	channels        map[int64]chan bool
	feedParser      *gofeed.Parser
	policy          *bluemonday.Policy
	postStmt        *sql.Stmt
	newPostStmt     *sql.Stmt
	newCategoryStmt *sql.Stmt
}

func NewPostFetcher(feedParser *gofeed.Parser, policy *bluemonday.Policy, db *sql.DB) *PostFetcher {
	pf := new(PostFetcher)
	pf.channels = make(map[int64]chan bool)
	pf.feedParser = feedParser
	pf.policy = policy

	postStmt, err := db.Prepare(`
	SELECT
		rowid
	FROM
		Post
	WHERE
		GUID = ?;
	`)
	if err != nil {
		log.Fatalf("spawnThreadsForFeedsInDB: prepare post query: %v", err)
	}
	pf.postStmt = postStmt

	newPostStmt, err := db.Prepare(`
	INSERT INTO 
		Post(GUID, Title, "Link", Excerpt, Content, PublicationDate, Author, ImageUrl, Feed_FK)
	VALUES
		    (?   , ?    , ?     , ?      , ?      , ?              , ?     , ?       , ?      );
	`)
	if err != nil {
		log.Fatalf("spawnThreadsForFeedsInDB: prepare new post query: %v", err)
	}
	pf.newPostStmt = newPostStmt

	newCategoryStmt, err := db.Prepare(`
	INSERT INTO 
		PostCategory(Post_FK, Category)
	VALUES
		            (?      , ?       );
	`)
	if err != nil {
		log.Fatalf("spawnThreadsForFeedsInDB: prepare new post category: %v", err)
	}
	pf.newCategoryStmt = newCategoryStmt

	return pf
}

func (pf PostFetcher) spawnThreadsFromDB(db *sql.DB) {
	rows, err := db.Query(`
	SELECT
		rowid,
		"Link",
		IntervalSeconds,
		DelaySeconds
	FROM
		Feed;
	`)
	if err != nil {
		log.Printf("spawnThreadsForFeedsInDB: get feed list from db: %v", err)
		return
	}

	type ThreadInfo struct {
		id       int64
		link     string
		interval time.Duration
		delay    time.Duration
	}

	var infos []ThreadInfo

	for rows.Next() {
		var id int64
		var link string
		var intervalSeconds, delaySeconds int
		err := rows.Scan(&id, &link, &intervalSeconds, &delaySeconds)
		if err != nil {
			log.Printf("spawnThreadsForFeedsInDB: scan feed row: %v", err)
			continue
		}

		var interval = time.Duration(intervalSeconds) * time.Second
		var delay = time.Duration(delaySeconds) * time.Second

		infos = append(infos, ThreadInfo{id, link, interval, delay})
	}

	rows.Close()

	for _, ti := range infos {
		go pf.spawnThread(ti.id, ti.link, ti.interval, ti.delay)
	}
}

func (pf PostFetcher) spawnThread(feedID int64, link string, interval time.Duration, delay time.Duration) {
	shouldClose, ok := pf.channels[feedID]
	if ok {
		shouldClose <- true
	} else {
		shouldClose = make(chan bool)
		pf.channels[feedID] = shouldClose
	}
	var feed *gofeed.Feed
	var err error

	for {
		feed, err = pf.feedParser.ParseURL(link)
		if err != nil {
			log.Printf("fetchPostsPeriodicallyFromFeed: parse feed %v: %v", link, err)
			return
		}

		skipInterval := false

		for _, item := range feed.Items {
			didFetch := pf.fetchPost(feedID, item)

			if didFetch {
				delayChan := time.After(delay)
				for {
					select {
					case ud := <-shouldClose:
						if ud {
							return
						} else {
							skipInterval = true
						}
					case <-delayChan:
					}
				}
			}
		}

		if !skipInterval {
			select {
			case ud := <-shouldClose:
				if ud {
					return
				}
			case <-time.After(interval):
			}
		} else {
			select {
			case ud := <-shouldClose:
				if ud {
					return
				}
			default:
			}
		}
	}
}

func (pf PostFetcher) KillThread(feedID int64) {
	pf.channels[feedID] <- true
	delete(pf.channels, feedID)
}

func (pf PostFetcher) fetchPost(feedID int64, item *gofeed.Item) bool {
	var article readability.Article
	var res sql.Result
	var row *sql.Row
	var rowid int64
	var err error

	row = pf.postStmt.QueryRow(item.GUID)

	err = row.Scan(&rowid)
	if err == nil {
		return false
	} else if err != sql.ErrNoRows {
		log.Printf("fetchPost: check if post exists: %v", err)
		return false
	}

	log.Printf("parsing post %v", item.Title)

	article, err = ParseArticle(item.Link, 30*time.Second)
	if err != nil {
		log.Printf("fetchPost: parse post %s: %v", item.Link, err)
		return true
	}

	var title string
	if item.Title != "" {
		title = item.Title
	} else {
		title = article.Title
	}

	var image string
	if item.Image != nil && item.Image.URL != "" {
		image = item.Image.URL
	} else {
		image = article.Image
	}

	var content string
	if item.Content != "" {
		content = item.Content
	} else {
		content = article.Content
	}

	// TODO: enable sanitization
	// content = policy.Sanitize(content)

	pubDate := time.Now().Unix()

	if item.PublishedParsed != nil {
		pubDate = item.PublishedParsed.Unix()
	}

	author := ""
	if item.Author != nil {
		author = item.Author.Name
	}

	res, err = pf.newPostStmt.Exec(item.GUID, title, item.Link, article.Excerpt, content, pubDate, author, image, feedID)
	if err != nil {
		if sqliteErr, ok := err.(sqlite3.Error); ok {
			if sqliteErr.Code == sqlite3.ErrConstraint {
				return true
			}
		}
		log.Printf("fetchPost: create post %s: %v", item.Link, err)
		return true
	}

	rowid, err = res.LastInsertId()

	for _, category := range item.Categories {
		_, err = pf.newCategoryStmt.Exec(rowid, category)
		if err != nil {
			log.Printf("fetchPost: add post category %s to %s: %v", category, item.Link, err)
			return true
		}
	}

	return true
}
