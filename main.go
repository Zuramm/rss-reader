package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"mime/multipart"
	"net/url"
	"os"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	"github.com/mattn/go-sqlite3"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mergestat/timediff"
	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"
)

const REFRESH_RATE = 1 * time.Hour

const (
	FEED_TYPE_RSS_ATOM = iota
	FEED_TYPE_WEB_SUB  = iota
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func removeUnordered[Type any](s []Type, i int) []Type {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

func removeOrdered[Type any](slice []Type, s int) []Type {
	return append(slice[:s], slice[s+1:]...)
}

type Post struct {
	Rowid           string
	Title           string
	Excerpt         string
	Link            string
	Content         string
	PublicationDate int64
	IsRead          bool
	Author          string
	FeedTitle       string
	ImageUrl        string
	Language        string
}

var fetchQuery struct {
	feedList        *sql.Stmt
	posts           *sql.Stmt
	newPost         *sql.Stmt
	newPostCategory *sql.Stmt
}

func initFetchQuery(db *sql.DB) {
	var err error

	feedListQuery, err := db.Prepare(`
	SELECT
		Title,
		"Link",
		IntervalSeconds,
		DelaySeconds
	FROM
		Feed;
	`)
	if err != nil {
		log.Fatalf("initFetchQuery: prepare feed list query: %v", err)
	}
	fetchQuery.feedList = feedListQuery

	postsQuery, err := db.Prepare(`
	SELECT
		rowid
	FROM
		Post
	WHERE
		GUID = ?;
	`)
	if err != nil {
		log.Fatalf("initFetchQuery: prepare post query: %v", err)
	}
	fetchQuery.posts = postsQuery

	newPostQuery, err := db.Prepare(`
	INSERT INTO Post(GUID, Title, "Link", Excerpt, Content, PublicationDate, Author, ImageUrl, FeedTitle)
		VALUES  (?,    ?,     ?,      ?,       ?,       ?,               ?,      ?,        ?        )
	`)
	if err != nil {
		log.Fatalf("initFetchQuery: prepare new post query: %v", err)
	}
	fetchQuery.newPost = newPostQuery

	newPostCategoryQuery, err := db.Prepare(`
	INSERT INTO PostCategory(Post_FK, Category)
		VALUES          (?,       ?       )
	`)
	if err != nil {
		log.Fatalf("initFetchQuery: prepare new post category: %v", err)
	}
	fetchQuery.newPostCategory = newPostCategoryQuery
}

func closeFetchQuery() {
	fetchQuery.feedList.Close()
	fetchQuery.posts.Close()
	fetchQuery.newPost.Close()
	fetchQuery.newPostCategory.Close()
}

func spawnThreadsForFeedsInDB(feedParser *gofeed.Parser, policy *bluemonday.Policy, channels map[string]chan bool) {
	// time.Sleep(5 * time.Minute)

	rows, err := fetchQuery.feedList.Query()
	if err != nil {
		log.Printf("fetchFeeds: get feed list from db: %v", err)
		return
	}

	var titleList, linkList []string
	var intervalList, delayList []time.Duration

	for rows.Next() {
		var title, link string
		var intervalSeconds, delaySeconds int
		err := rows.Scan(&title, &link, &intervalSeconds, &delaySeconds)
		if err != nil {
			log.Printf("fetchFeeds: scan feed row: %v", err)
			continue
		}

		var interval = time.Duration(intervalSeconds) * time.Second
		var delay = time.Duration(delaySeconds) * time.Second

		titleList = append(titleList, title)
		linkList = append(linkList, link)
		intervalList = append(intervalList, interval)
		delayList = append(delayList, delay)
	}

	rows.Close()

	for i := 0; i < len(linkList); i++ {
		shouldClose, ok := channels[titleList[i]]
		if ok {
			shouldClose <- true
		} else {
			shouldClose = make(chan bool)
			channels[titleList[i]] = shouldClose
		}
		go fetchPostsPeriodicallyFromFeed(feedParser, policy, linkList[i], intervalList[i], delayList[i], shouldClose)
	}
}

func fetchPostsPeriodicallyFromFeed(feedParser *gofeed.Parser, policy *bluemonday.Policy, link string, interval time.Duration, delay time.Duration, shouldClose chan bool) {
	var feed *gofeed.Feed
	var err error

	for {
		feed, err = feedParser.ParseURL(link)
		if err != nil {
			log.Printf("fetchFeedPosts: parse feed %v: %v", link, err)
			return
		}

		skipInterval := false

		for _, item := range feed.Items {
			didFetch := fetchPost(policy, feed, item)

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

func fetchPost(policy *bluemonday.Policy, feed *gofeed.Feed, item *gofeed.Item) bool {
	var article readability.Article
	var res sql.Result
	var row *sql.Row
	var rowid int64
	var err error

	row = fetchQuery.posts.QueryRow(item.GUID)

	err = row.Scan(&rowid)
	if err == nil {
		return false
	} else if err != sql.ErrNoRows {
		log.Printf("fetchFeedPosts: check if post exists: %v", err)
		return false
	}

	log.Printf("parsing post %v", item.Title)

	article, err = ParseArticle(item.Link, 30*time.Second)
	if err != nil {
		log.Printf("fetchFeedPosts: parse post %s: %v", item.Link, err)
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

	// content = policy.Sanitize(content)

	pubDate := time.Now().Unix()

	if item.PublishedParsed != nil {
		pubDate = item.PublishedParsed.Unix()
	}

	author := ""
	if item.Author != nil {
		author = item.Author.Name
	}

	res, err = fetchQuery.newPost.Exec(item.GUID, title, item.Link, article.Excerpt, content, pubDate, author, image, feed.Title)
	if err != nil {
		if sqliteErr, ok := err.(sqlite3.Error); ok {
			if sqliteErr.Code == sqlite3.ErrConstraint {
				return true
			}
		}
		log.Printf("fetchFeedPosts: create post %s: %v", item.Link, err)
		return true
	}

	rowid, err = res.LastInsertId()

	for _, category := range item.Categories {
		_, err = fetchQuery.newPostCategory.Exec(rowid, category)
		if err != nil {
			log.Printf("fetchFeedPosts: add post category %s to %s: %v", category, item.Link, err)
			return true
		}
	}

	return true
}

func convertArgs(args []string) []interface{} {
	ifaces := make([]interface{}, len(args))
	for i, v := range args {
		ifaces[i] = v
	}
	return ifaces
}

func main() {
	feedParser := gofeed.NewParser()

	policy := bluemonday.UGCPolicy()

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./feeds.db"
	}
	log.Printf("open database %v", dbPath)
	_, err := os.Stat(dbPath)
	initDb := os.IsNotExist(err)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("main: open database: %v", err)
	}
	defer db.Close()

	if initDb {
		log.Printf("creating new database")
		_, err := db.Exec(`
		CREATE TABLE Feed (
			Title TEXT NOT NULL,
			Link TEXT NOT NULL,
			"Type" INTEGER NOT NULL,
			"Language" TEXT,
			ImageUrl TEXT,
			ImageTitle TEXT, 
			Description TEXT NOT NULL,
			IntervalSeconds INTEGER DEFAULT 3600,
			DelaySeconds INTEGER DEFAULT 30,
			CONSTRAINT Feed_PK PRIMARY KEY (Title)
		);

		CREATE TABLE FeedCategory (
			FeedTitle TEXT NOT NULL,
			Category TEXT NOT NULL,
			CONSTRAINT FeedCategory_FK FOREIGN KEY (FeedTitle) REFERENCES Feed(Title) ON DELETE CASCADE ON UPDATE CASCADE,
			UNIQUE(FeedTitle, Category) ON CONFLICT REPLACE
		);

		CREATE TABLE Post (
			GUID TEXT NOT NULL UNIQUE ON CONFLICT IGNORE,
			Title TEXT NOT NULL,
			Link TEXT NOT NULL,
			Content TEXT NOT NULL,
			PublicationDate INTEGER NOT NULL,
			IsRead INTEGER DEFAULT(0) NOT NULL,
			Author TEXT,
			"FeedTitle" TEXT NOT NULL,
			ImageUrl TEXT,
			Excerpt TEXT,
			CONSTRAINT Post_FeedTitle_FK FOREIGN KEY ("FeedTitle") REFERENCES Feed (Title) ON DELETE CASCADE ON UPDATE CASCADE
		);

		CREATE VIRTUAL TABLE PostIdx USING fts5(Title, "Content", Author, content='Post');

		CREATE TABLE PostCategory (
			Post_FK TEXT NOT NULL,
			Category TEXT NOT NULL,
			CONSTRAINT PostCategory_Post_FK FOREIGN KEY (Post_FK) REFERENCES Post (rowid) ON DELETE CASCADE ON UPDATE CASCADE
		);
		`)
		if err != nil {
			log.Fatalf("main: init database: %v", err)
		}
	}

	log.Printf("init fetch queries")
	initFetchQuery(db)
	defer closeFetchQuery()

	log.Printf("spawning fetch threads")
	feedChannels := make(map[string]chan bool)
	spawnThreadsForFeedsInDB(feedParser, policy, feedChannels)

	log.Printf("initializing frontend")
	// Create a new engine
	viewsPath := os.Getenv("VIEWS_PATH")
	if viewsPath == "" {
		viewsPath = "./views"
	}
	engine := html.New(viewsPath, ".html")
	engine.AddFunc("pathEscape", url.PathEscape)
	engine.AddFunc("htmlSafe", func(html string) template.HTML {
		return template.HTML(html)
	})
	engine.AddFunc("datetime", func(timestamp int64) template.HTML {
		t := time.Unix(timestamp, 0)
		return template.HTML(
			fmt.Sprintf(
				"<time datetime=\"%s\">%s</time>",
				t.Format(time.RFC3339),
				t.Format(time.UnixDate),
			),
		)
	})

	engine.AddFunc("reltime", func(timestamp int64) template.HTML {
		t := time.Unix(timestamp, 0)
		return template.HTML(
			fmt.Sprintf(
				"<time datetime=\"%s\">%s</time>",
				t.Format(time.RFC3339),
				timediff.TimeDiff(t),
			),
		)
	})

	// Pass the engine to the Views
	app := fiber.New(fiber.Config{
		Views:       engine,
		ViewsLayout: "layout/main",
	})

	app.Static("/", "./public")

	allFeedsTitle, err := db.Prepare(`
	SELECT
		Title
	FROM
		Feed
	ORDER BY
		Title ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare all feeds title: %v", err)
	}
	defer allFeedsTitle.Close()

	allFeedCategoriesQuery, err := db.Prepare(`
	SELECT DISTINCT
		Category
	FROM
		FeedCategory
	ORDER BY
		Category ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare all feed categories: %v", err)
	}
	defer allFeedCategoriesQuery.Close()

	allPostCategoriesQuery, err := db.Prepare(`
	SELECT
		Category
	FROM
		PostCategory
	GROUP BY
		Category
	HAVING
		COUNT(Post_FK) > 2
	ORDER BY
		Category ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare all post categories: %v", err)
	}
	defer allPostCategoriesQuery.Close()

	allPostQueryStr := `
	SELECT
		Post.rowid,
		Post.Title,
		Excerpt,
		PublicationDate,
		IsRead,
		Post.Author,
		FeedTitle,
		Post.ImageUrl,
		Language
	FROM
		Post
	LEFT JOIN Feed ON Post.FeedTitle = Feed.Title
	%s;
	`

	allPostQueryCountStr := `
	SELECT
		Count(*)
	FROM
		Post
	%s;
	`

	allPostQuerySearchStr := `
		INNER JOIN PostIdx ON Post.rowid = PostIdx.rowid
	WHERE
		PostIdx MATCH ?
	`

	allPostQueryFeedTitleStr := `
		FeedTitle IN(%s)
	`

	allPostQueryFeedCategoryStr := `
	FeedTitle IN(
		SELECT
			FeedTitle FROM FeedCategory
		WHERE
			Category IN(%s)
	)
	`

	allPostQueryPostCategoryStr := `
	rowid IN(
		SELECT
			Post_FK FROM PostCategory
		WHERE
			Category IN(%s)
	)
	`

	allPostQueryIsNotReadStr := `
	IsRead = 0
	`

	allPostQuerySortPubDateDescStr := `
	ORDER BY
		PublicationDate DESC
	`

	allPostQuerySortPubDateAscStr := `
	ORDER BY
		PublicationDate ASC
	`

	allPostQueryPaginationStr := `
	LIMIT ? OFFSET ?
	`

	pageSize := 24

	app.Get("/", func(c *fiber.Ctx) error {
		query := c.Context().QueryArgs()

		type TitleSelected struct {
			Title    string
			Selected bool
		}

		var selectedFeedTitles, selectedFeedCategories, selectedPostCategories []string
		var feeds, feedCategories, postCategories []TitleSelected

		rows, err := allFeedsTitle.Query()
		if err != nil {
			log.Printf("GET /: get all feeds")
		} else {
			for rows.Next() {
				var feed string
				err := rows.Scan(&feed)
				if err != nil {
					log.Printf("GET /: get feed data")
				}

				isSet := false

				for _, el := range query.PeekMulti("feed") {
					if string(el) == feed {
						isSet = true
						break
					}
				}

				if isSet {
					selectedFeedTitles = append(selectedFeedTitles, feed)
				}

				feeds = append(feeds, TitleSelected{feed, isSet})
			}
		}

		rows, err = allFeedCategoriesQuery.Query()
		if err != nil {
			log.Printf("GET /: get all feed categories")
		} else {
			for rows.Next() {
				var category string
				err := rows.Scan(&category)
				if err != nil {
					log.Printf("GET /: get feed category data")
				}

				isSet := false

				for _, el := range query.PeekMulti("feedCategory") {
					if string(el) == category {
						isSet = true
						break
					}
				}

				if isSet {
					selectedFeedCategories = append(selectedFeedCategories, category)
				}

				feedCategories = append(feedCategories, TitleSelected{category, isSet})
			}
		}

		rows, err = allPostCategoriesQuery.Query()
		if err != nil {
			log.Printf("GET /: get all post categories")
		} else {
			for rows.Next() {
				var category string
				err := rows.Scan(&category)
				if err != nil {
					log.Printf("GET /: get post category data")
				}

				isSet := false

				for _, el := range query.PeekMulti("postCategory") {
					if string(el) == category {
						isSet = true
						break
					}
				}

				if isSet {
					selectedPostCategories = append(selectedPostCategories, category)
				}

				postCategories = append(postCategories, TitleSelected{category, isSet})
			}
		}

		wherestr := ""
		orderstr := allPostQuerySortPubDateDescStr
		var values []interface{}

		queryTerm := string(query.Peek("query"))

		if len(queryTerm) > 0 {
			wherestr = allPostQuerySearchStr
			values = append(values, queryTerm)
		}

		if len(selectedFeedTitles) > 0 {
			if len(wherestr) == 0 {
				wherestr += "WHERE "
			} else {
				wherestr += " AND "
			}

			placeholders := strings.Repeat("?,", len(selectedFeedTitles)-1) + "?"

			wherestr += fmt.Sprintf(allPostQueryFeedTitleStr, placeholders)
			values = append(values, convertArgs(selectedFeedTitles)...)
		}

		if len(selectedFeedCategories) > 0 {
			if len(wherestr) == 0 {
				wherestr += "WHERE "
			} else {
				wherestr += " AND "
			}

			placeholders := strings.Repeat("?,", len(selectedFeedCategories)-1) + "?"

			wherestr += fmt.Sprintf(allPostQueryFeedCategoryStr, placeholders)
			values = append(values, convertArgs(selectedFeedCategories)...)
		}

		if len(selectedPostCategories) > 0 {
			if len(wherestr) == 0 {
				wherestr += "WHERE "
			} else {
				wherestr += " AND "
			}

			placeholders := strings.Repeat("?,", len(selectedPostCategories)-1) + "?"

			wherestr += fmt.Sprintf(allPostQueryPostCategoryStr, placeholders)
			values = append(values, convertArgs(selectedPostCategories)...)
		}

		showAll := string(query.Peek("allPosts")) == "on"

		if !showAll {
			if len(wherestr) == 0 {
				wherestr += "WHERE "
			} else {
				wherestr += " AND "
			}

			wherestr += allPostQueryIsNotReadStr
		}

		oldestFirst := string(query.Peek("oldestFirst")) == "on"

		if oldestFirst {
			orderstr = allPostQuerySortPubDateAscStr
		}

		page := query.GetUintOrZero("page")

		countstr := fmt.Sprintf(allPostQueryCountStr, wherestr)

		row := db.QueryRow(countstr, values...)

		count := 0
		maxPage := 100_000_000
		err = row.Scan(&count)
		if err != nil {
			log.Printf("GET /: count results: %v", err)
		}

		if count > 0 {
			maxPage = count / pageSize
			page = min(page, maxPage)
		}

		querystr := fmt.Sprintf(allPostQueryStr, wherestr+orderstr+allPostQueryPaginationStr)
		values = append(values, pageSize, page*pageSize)

		rows, err = db.Query(querystr, values...)
		if err != nil {
			log.Printf("GET /: get all posts: %v", err)
			return c.Render("status", fiber.Map{
				"Title": "Error",
				"Name":  "Failed Getting Posts",
			})
		}
		defer rows.Close()

		var posts []Post

		for rows.Next() {
			var post Post
			err := rows.Scan(&post.Rowid, &post.Title, &post.Excerpt, &post.PublicationDate, &post.IsRead, &post.Author, &post.FeedTitle, &post.ImageUrl, &post.Language)
			if err != nil {
				log.Printf("GET /: get post data: %v", err)
				continue
			}

			posts = append(posts, post)
		}

		// Render with and extends
		return c.Render("postList", fiber.Map{
			"Styles":         []string{"/post-list.css"},
			"Title":          "All Posts",
			"FeedCategories": feedCategories,
			"PostCategories": postCategories,
			"Feeds":          feeds,
			"Posts":          posts,
			"OldestFirst":    oldestFirst,
			"AllPosts":       showAll,
			"Query":          queryTerm,
			"Page":           page,
			"PagePrev":       max(0, page-1),
			"PageNext":       min(page+1, maxPage),
			"Results":        count,
		})
	})

	postQuery, err := db.Prepare(`
	SELECT
		Post.Title,
		Post.Link,
		Content,
		PublicationDate,
		Author,
		FeedTitle,
		Post.ImageUrl,
		Language
	FROM
		Post
	LEFT JOIN Feed ON Post.FeedTitle = Feed.Title
	WHERE
		Post.rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare post query: %v", err)
	}
	defer postQuery.Close()

	readPostQuery, err := db.Prepare(`
	UPDATE
		Post
	SET
		IsRead = 1
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare read post query: %v", err)
	}
	defer readPostQuery.Close()

	postCategoryQuery, err := db.Prepare(`
	SELECT
		Category
	FROM
		PostCategory
	WHERE
		Post_FK = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare post category query: %v", err)
	}
	defer postCategoryQuery.Close()

	app.Get("/post/:id", func(c *fiber.Ctx) error {
		var rows *sql.Rows
		var row *sql.Row
		var id int
		var err error

		id, err = c.ParamsInt("id")
		if err != nil {
			log.Printf("GET /post/:id: get title: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": "Invalid title",
			})
		}

		_, err = readPostQuery.Exec(id)
		if err != nil {
			log.Printf("GET /post/:id: set post as read: %v", err)
		}

		row = postQuery.QueryRow(id)

		var post Post

		err = row.Scan(&post.Title, &post.Link, &post.Content, &post.PublicationDate, &post.Author, &post.FeedTitle, &post.ImageUrl, &post.Language)
		if err != nil {
			log.Printf("GET /post/:id: get post data: %v", err)
			return c.Render("status", fiber.Map{
				"Title": "Error",
				"Name":  "Failed Getting Post",
			})
		}

		var categories []string

		rows, err = postCategoryQuery.Query(id)
		if err != nil {
			log.Printf("GET /post/:id: get all categories: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Post",
				"Description": "Failed Getting Post Categories",
			})
		}

		for rows.Next() {
			var category string
			err = rows.Scan(&category)
			if err != nil {
				log.Printf("GET /post/:id: get category data: %v", err)
				continue
			}

			categories = append(categories, category)
		}

		return c.Render("post", fiber.Map{
			"Styles":     []string{"/post.css"},
			"Title":      post.Title,
			"Post":       post,
			"Categories": categories,
			"Date":       post.PublicationDate,
			"Content":    template.HTML(post.Content)},
		)
	})

	postAllDataQuery, err := db.Prepare(`
	SELECT
		GUID,
		Title,
		Link,
		Content,
		PublicationDate,
		IsRead,
		Author,
		FeedTitle,
		ImageUrl,
		Excerpt
	FROM
		Post
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare post all data query: %v", err)
	}
	defer postAllDataQuery.Close()

	updatePostAllDataQuery, err := db.Prepare(`
	UPDATE
		Post
	SET
		GUID = ?,
		Title = ?,
		Link = ?,
		Content = ?,
		PublicationDate = ?,
		IsRead = ?,
		Author = ?,
		FeedTitle = ?,
		ImageUrl = ?,
		Excerpt = ?
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare update post all data query: %v", err)
	}
	defer updatePostAllDataQuery.Close()

	app.Post("/post/:id", func(c *fiber.Ctx) error {
		var row *sql.Row
		var id, PublicationDate, IsRead int
		var GUID, Title, Link, Content, Author, FeedTitle, ImageUrl, Excerpt string
		var article readability.Article
		var err error

		id, err = c.ParamsInt("id")
		if err != nil {
			log.Printf("POST /post/%v: get post id: %v", id, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Invalid ID",
			})
		}

		row = postAllDataQuery.QueryRow(id)

		err = row.Scan(&GUID, &Title, &Link, &Content, &PublicationDate, &IsRead, &Author, &FeedTitle, &ImageUrl, &Excerpt)
		if err != nil {
			log.Printf("POST /post/%v: getting all post data: %v", id, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Couldn't load data",
			})
		}

		article, err = ParseArticle(Link, 30*time.Second)
		if err != nil {
			log.Printf("POST /post/%v: parsing article: %v", id, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Couldn't parse article",
			})
		}

		if article.Title != "" {
			Title = article.Title
		}

		if article.Content != "" {
			Content = article.Content
		}

		if article.Image != "" {
			ImageUrl = article.Image
		}

		if article.Excerpt != "" {
			Excerpt = article.Excerpt
		}

		_, err = updatePostAllDataQuery.Exec(GUID, Title, Link, Content, PublicationDate, IsRead, Author, FeedTitle, ImageUrl, Excerpt, id)
		if err != nil {
			log.Printf("POST /post/%v: updating post: %v", id, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Couldn't parse article",
			})
		}

		return c.Redirect(fmt.Sprintf("/post/%v", id))
	})

	allFeedsQuery, err := db.Prepare(`
	SELECT
		Title,
		Description,
		"Link",
		"Language",
		ImageUrl,
		ImageTitle
	FROM
		Feed
	ORDER BY
		Title ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare all feeds query: %v", err)
	}
	defer allFeedsQuery.Close()

	app.Get("/feed", func(c *fiber.Ctx) error {
		rows, err := allFeedsQuery.Query()
		if err != nil {
			log.Printf("GET /feed: get all feeds: %v", err)
			return c.Render("status", fiber.Map{
				"Title": "Error",
				"Name":  "Failed Loading Feeds",
			})
		}
		defer rows.Close()

		type Feed struct {
			Title       string
			Description string
			Link        string
			Language    string
			ImageUrl    string
			ImageTitle  string
		}

		var feeds []Feed

		for rows.Next() {
			var feed Feed
			err := rows.Scan(&feed.Title, &feed.Description, &feed.Link, &feed.Language, &feed.ImageUrl, &feed.ImageTitle)
			if err != nil {
				log.Printf("GET /feed: get feed data: %v", err)
				continue
			}
			feeds = append(feeds, feed)
		}

		return c.Render("feedList", fiber.Map{
			"Styles": []string{"/feed-list.css"},
			"Title":  "All Feeds",
			"Feeds":  feeds,
		})
	})

	newFeedQuery, err := db.Prepare(`
	INSERT INTO Feed(Title, Description, Link, Type, Language, ImageUrl, ImageTitle)
	    VALUES      (?,     ?,        ?,       ?,    ?,        ?,        ?         )
	`)
	if err != nil {
		log.Fatalf("main: prepare new feed query: %v", err)
	}
	defer newFeedQuery.Close()

	app.Post("/feed", func(c *fiber.Ctx) error {
		rssUrl := c.FormValue("url")
		if rssUrl == "" {
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed to Create Feed",
				"Description": "Missing RSS-URL",
			})
		}

		feed, err := feedParser.ParseURL(rssUrl)
		if err != nil {
			log.Printf("POST /feed: parse feed: %v", err)
			return c.Render("status", fiber.Map{
				"Title":  "Error",
				"Name":   "Failed to Create Feed",
				"Reason": fmt.Sprintf("Failed parsing RSS-Feed \"%s\"", rssUrl),
			})
		}

		feedLink := feed.FeedLink
		if feedLink == "" {
			feedLink = rssUrl
		}

		_, err = newFeedQuery.Exec(feed.Title, feed.Description, feedLink, 0, feed.Language, "", "") // feed.Image.URL, feed.Image.Title)
		if err != nil {
			log.Printf("POST /feed: add feed: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed to Create Feed",
				"Description": "Failed database query",
			})
		}

		// NOTE: assume that a new feed was created (none existed before and db query was succesfull)
		shouldClose := make(chan bool)
		feedChannels[rssUrl] = shouldClose
		go fetchPostsPeriodicallyFromFeed(feedParser, policy, rssUrl, time.Duration(3600)*time.Second, time.Duration(30)*time.Second, shouldClose)

		return c.Render("status", fiber.Map{
			"Title":       "Added Feed",
			"Name":        "Added Feed Successfully",
			"Description": fmt.Sprintf("Added feed with title %v", feed.Title),
		})
	})

	feedQuery, err := db.Prepare(`
	SELECT
		Title,
		Description,
		"Link",
		"Language",
		ImageUrl,
		ImageTitle,
		IntervalSeconds,
		DelaySeconds
	FROM
		Feed
	WHERE
		Title = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare feed query: %v", err)
	}
	defer feedQuery.Close()

	feedCategoriesByTitleQuery, err := db.Prepare(`
	SELECT DISTINCT
		Category,
		CASE WHEN EXISTS (
			SELECT
				1
			FROM
				FeedCategory
			WHERE
				Category = t.Category
			AND FeedTitle = ?)
		THEN
			1
		ELSE
			0
		END AS TitleExists
	FROM
		FeedCategory t
	ORDER BY
		Category ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare feed categories query: %v", err)
	}
	defer feedCategoriesByTitleQuery.Close()

	feedLanguagesQuery, err := db.Prepare(`
	SELECT DISTINCT
		Language
	FROM
		Feed
	ORDER BY
		Language ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare feed languages query: %v", err)
	}
	defer feedLanguagesQuery.Close()

	app.Get("/feed/:title", func(c *fiber.Ctx) error {
		title, err := url.PathUnescape(c.Params("title"))
		if err != nil {
			log.Printf("GET /feed/:title: get title: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": "Invalid title",
			})
		}

		row := feedQuery.QueryRow(title)

		type Feed struct {
			Title       string
			Description string
			Link        string
			Type        int
			Language    string
			ImageUrl    string
			ImageTitle  string
			Interval    string
			Delay       string
		}

		var feed Feed
		var intervalSeconds, delaySeconds int

		err = row.Scan(&feed.Title, &feed.Description, &feed.Link, &feed.Language, &feed.ImageUrl, &feed.ImageTitle, &intervalSeconds, &delaySeconds)
		if err != nil {
			log.Printf("GET /feed/:title: scan feed row: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": fmt.Sprintf("Couldn't load data for \"%s\"", title),
			})
		}

		feed.Interval = (time.Duration(intervalSeconds) * time.Second).String()
		feed.Delay = (time.Duration(delaySeconds) * time.Second).String()

		rows, err := feedCategoriesByTitleQuery.Query(title)
		if err != nil {
			log.Printf("GET /feed/:title: get feed categories: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": "Couldn't load Categories",
			})
		}
		defer rows.Close()

		type Item struct {
			Name   string
			Active bool
		}

		var categories []Item

		for rows.Next() {
			var value Item
			err = rows.Scan(&value.Name, &value.Active)
			if err != nil {
				log.Printf("GET /feed/:title: scan feed category data: %v", err)
				continue
			}

			categories = append(categories, value)
		}

		rows, err = feedLanguagesQuery.Query()
		if err != nil {
			log.Printf("GET /feed/:title: get feed categories: %v", err)
		}

		var languageSuggestions []string

		if rows != nil {
			for rows.Next() {
				var value string
				err = rows.Scan(&value)
				if err != nil {
					log.Printf("GET /feed/:title: scan feed languages: %v", err)
				}

				languageSuggestions = append(languageSuggestions, value)
			}
		}

		return c.Render("feed", fiber.Map{
			"Styles":              []string{"/feed.css"},
			"Title":               feed.Title,
			"Feed":                feed,
			"Categories":          categories,
			"Language":            feed.Language,
			"LanguageSuggestions": languageSuggestions,
		})
	})

	removeFeedQuery, err := db.Prepare(`
	DELETE FROM Feed
	WHERE Title = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare remove feed query: %v", err)
	}
	defer removeFeedQuery.Close()

	updateFeedQuery, err := db.Prepare(`
	UPDATE
		Feed
	SET
		Title = ?,
		Description = ?,
		"Link" = ?,
		IntervalSeconds = ?,
		DelaySeconds = ?
	WHERE
		Title = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare update feed query: %v", err)
	}
	defer updateFeedQuery.Close()

	addFeedCategoryQuery, err := db.Prepare(`
	INSERT INTO FeedCategory(FeedTitle, Category)
		VALUES          (?        , ?       )
	`)
	if err != nil {
		log.Fatalf("main: prepare add feed category query: %v", err)
	}
	defer updateFeedQuery.Close()

	removeFeedCategoryQuery, err := db.Prepare(`
	DELETE FROM FeedCategory
	WHERE FeedTitle = ?
	AND Category = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare remove feed category query: %v", err)
	}
	defer removeFeedCategoryQuery.Close()

	app.Post("/feed/:title", func(c *fiber.Ctx) error {
		var err error
		var title string
		var form *multipart.Form

		title, err = url.PathUnescape(c.Params("title"))
		if err != nil {
			log.Printf("POST /feed/:title: get title from param: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Feed Operation",
				"Description": "Invalid feed title",
			})
		}

		form, err = c.MultipartForm()
		if err != nil {
			log.Printf("POST /feed/:title: get multipart form: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Feed Operation",
				"Description": "Invalid multipart form",
			})
		}

		var method string

		if len(form.Value["method"]) > 0 {
			method = form.Value["method"][0]
		}

		switch method {
		case "delete":
			_, err = removeFeedQuery.Exec(title)
			if err != nil {
				log.Printf("POST /feed/:title: remove feed %v: %v", title, err)
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed to Remove Feed",
					"Description": "Server error",
				})
			}

			feedChannels[title] <- true
			delete(feedChannels, title)

			return c.Render("status", fiber.Map{
				"Title":       "Deleted Feed",
				"Name":        "Deleted Feed Successfully",
				"Description": fmt.Sprintf("Deleted feed with title %v", title),
			})
		default:
			if len(form.Value["title"]) == 0 || len(form.Value["description"]) == 0 || len(form.Value["link"]) == 0 || len(form.Value["interval"]) == 0 || len(form.Value["delay"]) == 0 {
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed Updating Feed",
					"Description": "Incomplete form data",
				})
			}

			log.Print("update feed start")

			interval, err := time.ParseDuration(form.Value["interval"][0])
			if err != nil {
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed Updating Feed",
					"Description": "Incomplete form data",
				})
			}
			delay, err := time.ParseDuration(form.Value["delay"][0])
			if err != nil {
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed Updating Feed",
					"Description": "Incomplete form data",
				})
			}

			log.Print("update feed in db")

			_, err = updateFeedQuery.Exec(form.Value["title"][0], form.Value["description"][0], form.Value["link"][0], interval.Seconds(), delay.Seconds(), title)
			if err != nil {
				log.Printf("POST /feed/:title: update feed: %v", err)
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed Updateting Feed",
					"Description": "Server error",
				})
			}

			log.Print("close channel")
			feedChannels[title] <- true
			log.Print("start thread")
			go fetchPostsPeriodicallyFromFeed(feedParser, policy, form.Value["title"][0], interval, delay, feedChannels[title])
			feedChannels[form.Value["title"][0]] = feedChannels[title]
			delete(feedChannels, title)

			// the query rows have to be closed before making further operations on the same table
			var addCat, removeCat []string

			addCat = form.Value["category"]

			log.Print("get previous categories")
			{
				rows, err := feedCategoriesByTitleQuery.Query(title)
				if err != nil {
					log.Printf("POST /feed/:title: get categories: %v", err)
					return c.Render("status", fiber.Map{
						"Title":       "Error",
						"Name":        "Failed Updateting Feed",
						"Description": "Server error",
					})
				}
				defer rows.Close()

				for rows.Next() {
					var category string
					var isAssignedInTable bool
					err := rows.Scan(&category, &isAssignedInTable)
					if err != nil {
						log.Printf("POST /feed/:title: read category data: %v", err)
						continue
					}

					categoryIndex := -1

					for i, cat := range form.Value["category"] {
						if cat == category {
							categoryIndex = i
							break
						}
					}

					isAssignedInForm := categoryIndex >= 0

					if isAssignedInTable && !isAssignedInForm {
						removeCat = append(removeCat, category)
					} else if isAssignedInTable {
						addCat = removeUnordered(addCat, categoryIndex)
					}
				}
			}

			log.Print("update categories")
			for _, category := range addCat {
				if category == "" {
					continue
				}
				_, err := addFeedCategoryQuery.Exec(title, category)
				if err != nil {
					log.Printf("POST /feed/:title: add category %v: %v", category, err)
				}
			}

			for _, category := range removeCat {
				_, err := removeFeedCategoryQuery.Exec(title, category)
				if err != nil {
					log.Printf("POST /feed/:title: remove category %v: %v", category, err)
				}
			}

			log.Print("render response")
			return c.Render("status", fiber.Map{
				"Title":       "Updated Feed",
				"Name":        "Updated Feed Successfully",
				"Description": fmt.Sprintf("Updated feed with title %v", title),
			})
		}
	})

	log.Fatal(app.Listen(":3000"))
}
