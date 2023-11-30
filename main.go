package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"mime/multipart"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	"github.com/mattn/go-sqlite3"
	_ "github.com/mattn/go-sqlite3"
	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"
)

const REFRESH_RATE = 1 * time.Hour

const (
	FEED_TYPE_RSS_ATOM = iota
	FEED_TYPE_WEB_SUB  = iota
)

type Feed struct {
	Title       string
	Description string
	Link        string
	Type        int
	Language    string
	ImageUrl    string
	ImageTitle  string
}

type Post struct {
	GUID            string
	Title           string
	Excerpt         string
	Link            string
	Content         string
	PublicationDate int64
	IsRead          bool
	Author          string
	FeedTitle       string
	ImageUrl        string
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
            "Link"
        FROM
            Feed;
    `)
	if err != nil {
		log.Fatalf("initFetchQuery: prepare feed list query: %v", err)
	}
	fetchQuery.feedList = feedListQuery

	postsQuery, err := db.Prepare(`
        SELECT
            GUID
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
            VALUES      (?,    ?,     ?,    ?,       ?,       ?,               ?,      ?,        ?     )
    `)
	if err != nil {
		log.Fatalf("initFetchQuery: prepare new post query: %v", err)
	}
	fetchQuery.newPost = newPostQuery

	newPostCategoryQuery, err := db.Prepare(`
        INSERT INTO PostCategory(PostGUID, Category)
            VALUES              (?       , ?       )
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

func fetchFeeds(feedParser *gofeed.Parser, policy *bluemonday.Policy, db *sql.DB) {
	for {
		var links []string

		{
			rows, err := fetchQuery.feedList.Query()
			if err != nil {
				log.Printf("fetchFeeds: get feed list from db: %v", err)
				return
			}
			defer rows.Close()

			for rows.Next() {
				var link string
				err := rows.Scan(&link)
				if err != nil {
					log.Printf("fetchFeeds: scan feed row: %v", err)
					continue
				}

				links = append(links, link)
			}
		}

		for _, link := range links {
			fetchFeedPosts(feedParser, policy, link)
		}

		time.Sleep(REFRESH_RATE)
	}
}

func fetchFeedPosts(feedParser *gofeed.Parser, policy *bluemonday.Policy, link string) {
	feed, err := feedParser.ParseURL(link)
	if err != nil {
		log.Printf("fetchFeedPosts: parse feed %v: %v", link, err)
		return
	}

	for _, item := range feed.Items {
		article, err := readability.FromURL(item.Link, 30*time.Second)
		if err != nil {
			log.Printf("fetchFeedPosts: parse post %s: %v", item.Link, err)
			continue
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

		_, err = fetchQuery.newPost.Exec(item.GUID, title, item.Link, article.Excerpt, content, pubDate, author, image, feed.Title)
		if err != nil {
			if sqliteErr, ok := err.(sqlite3.Error); ok {
				if sqliteErr.Code == sqlite3.ErrConstraint {
					continue
				}
			}
			log.Printf("fetchFeedPosts: create post %s: %v", item.Link, err)
			continue
		}

		for _, category := range item.Categories {
			_, err = fetchQuery.newPostCategory.Exec(item.GUID, category)
			if err != nil {
				log.Printf("fetchFeedPosts: add post category %s to %s: %v", category, item.GUID, err)
				continue
			}
		}
	}
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

	log.Print("open database")
	db, err := sql.Open("sqlite3", "./feeds")
	if err != nil {
		log.Fatalf("main: open database: %v", err)
	}
	defer db.Close()

	initFetchQuery(db)
	defer closeFetchQuery()

	go fetchFeeds(feedParser, policy, db)

	// Create a new engine
	engine := html.New("./views", ".html")
	engine.AddFunc("pathEscape", url.PathEscape)
	engine.AddFunc("htmlSafe", func(html string) template.HTML {
		return template.HTML(html)
	})

	// Pass the engine to the Views
	app := fiber.New(fiber.Config{
		Views:       engine,
		ViewsLayout: "layout/main",
	})

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
        SELECT DISTINCT
            Category
        FROM
            PostCategory
        ORDER BY
            Category ASC;
    `)
	if err != nil {
		log.Fatalf("main: prepare all post categories: %v", err)
	}
	defer allPostCategoriesQuery.Close()

	allPostQueryStr := `
        SELECT
            GUID,
            Post.Title,
            Excerpt,
            PublicationDate,
            IsRead,
            Post.Author,
            FeedTitle,
            ImageUrl
        FROM
            Post
        %s;
    `

	allPostQuerySearchStr := `
            INNER JOIN PostIdx
        WHERE
	        PostIdx MATCH ?
	        AND Post.rowid = PostIdx.rowid
    `

	allPostQueryFeedTitleStr := `
        FeedTitle IN(%s)
    `

	allPostQueryFeedCategoryStr := `
        FeedTitle IN(
            SELECT
                FeedTitle FROM FeedCategory
            WHERE
                Category IN(%s))
    `

	allPostQueryPostCategoryStr := `
        GUID IN(
            SELECT
                PostGUID as GUID FROM PostCategory
            WHERE
                Category IN(%s))
    `

	allPostQueryIsNotReadStr := `
        IsRead = 0
    `

	allPostQuerySortPubDateDesc := `
        ORDER BY
            PublicationDate DESC
    `

	allPostQuerySortPubDateAsc := `
        ORDER BY
            PublicationDate ASC
    `

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
		orderstr := allPostQuerySortPubDateDesc
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
			orderstr = allPostQuerySortPubDateAsc
		}

		querystr := fmt.Sprintf(allPostQueryStr, wherestr+orderstr)

		rows, err = db.Query(querystr, values...)
		if err != nil {
			log.Printf("GET /: get all posts: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Getting Posts"})
		}
		defer rows.Close()

		var posts []Post

		for rows.Next() {
			var post Post
			var author, excerpt, imageUrl *string
			err := rows.Scan(&post.GUID, &post.Title, &excerpt, &post.PublicationDate, &post.IsRead, &author, &post.FeedTitle, &imageUrl)
			if err != nil {
				log.Printf("GET /: get post data: %v", err)
				continue
			}

			if author != nil {
				post.Author = *author
			}

			if excerpt != nil {
				post.Excerpt = *excerpt
			}

			if imageUrl != nil {
				post.ImageUrl = *imageUrl
			}

			posts = append(posts, post)
		}

		// Render with and extends
		return c.Render("postList", fiber.Map{
			"FeedCategories": feedCategories,
			"PostCategories": postCategories,
			"Feeds":          feeds,
			"Posts":          posts,
			"OldestFirst":    oldestFirst,
			"AllPosts":       showAll,
			"Query":          queryTerm,
		})
	})

	postQuery, err := db.Prepare(`
        SELECT
            Title,
            Link,
            Content,
            PublicationDate,
            Author,
            FeedTitle,
            ImageUrl
        FROM
            Post
        WHERE
            GUID = ?;
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
            GUID = ?;
    `)
	if err != nil {
		log.Fatalf("main: prepare read post query: %v", err)
	}
	defer readPostQuery.Close()

	app.Get("/post/:guid", func(c *fiber.Ctx) error {
		guid, err := url.PathUnescape(c.Params("guid"))
		if err != nil {
			log.Printf("GET /post/:guid: get title: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Getting Feed", "Reason": "Invalid title"})
		}

		_, err = readPostQuery.Exec(guid)
		if err != nil {
			log.Printf("GET /post/:guid: set post as read: %v", err)
		}

		rows := postQuery.QueryRow(guid)

		var post Post

		err = rows.Scan(&post.Title, &post.Link, &post.Content, &post.PublicationDate, &post.Author, &post.FeedTitle, &post.ImageUrl)
		if err != nil {
			log.Printf("GET /post/:guid: get post data: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Getting Post"})
		}

		return c.Render("post", fiber.Map{"Post": post, "Content": template.HTML(post.Content)})
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
			return c.Render("error", fiber.Map{"Error": "Failed Loading Feeds"})
		}
		defer rows.Close()

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

		return c.Render("feedList", fiber.Map{"Feeds": feeds})
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
			return c.Render("error", fiber.Map{"Error": "Failed to Create Feed", "Reason": "Missing RSS-URL"})
		}

		feed, err := feedParser.ParseURL(rssUrl)
		if err != nil {
			log.Printf("POST /feed: parse feed: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed to Create Feed", "Reason": fmt.Sprintf("Failed parsing RSS-Feed \"%s\"", rssUrl)})
		}

		feedLink := feed.FeedLink
		if feedLink == "" {
			feedLink = rssUrl
		}

		_, err = newFeedQuery.Exec(feed.Title, feed.Description, feedLink, 0, feed.Language, "", "") // feed.Image.URL, feed.Image.Title)
		if err != nil {
			log.Printf("POST /feed: add feed: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed to Create Feed", "Reason": "Failed database query"})
		}

		go fetchFeedPosts(feedParser, policy, feedLink)

		return c.Render("feedAdd", fiber.Map{"Title": feed.Title})
	})

	feedQuery, err := db.Prepare(`
        SELECT
            Title,
            Description,
            "Link",
            "Language",
            ImageUrl,
            ImageTitle
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
                    AND FeedTitle = ?) THEN
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

	app.Get("/feed/:title", func(c *fiber.Ctx) error {
		title, err := url.PathUnescape(c.Params("title"))
		if err != nil {
			log.Printf("GET /feed/:title: get title: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Getting Feed", "Reason": "Invalid title"})
		}

		row := feedQuery.QueryRow(title)

		var feed Feed

		err = row.Scan(&feed.Title, &feed.Description, &feed.Link, &feed.Language, &feed.ImageUrl, &feed.ImageTitle)
		if err != nil {
			log.Printf("GET /feed/:title: scan feed row: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Getting Feed", "Reason": fmt.Sprintf("Couldn't load data for \"%s\"", title)})
		}

		rows, err := feedCategoriesByTitleQuery.Query(title)
		if err != nil {
			log.Printf("GET /feed/:title: get feed categories: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Getting Feed", "Reason": "Couldn't load Categories"})
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

		return c.Render("feed", fiber.Map{"Feed": feed, "Categories": categories})
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
            "Link" = ?
        WHERE
            Title = ?;
    `)
	if err != nil {
		log.Fatalf("main: prepare update feed query: %v", err)
	}
	defer updateFeedQuery.Close()

	addFeedCategoryQuery, err := db.Prepare(`
        INSERT INTO FeedCategory(FeedTitle, Category)
            VALUES              (?        , ?       )
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
			return c.Render("error", fiber.Map{"Error": "Failed Feed Operation", "Reason": "Invalid feed title"})
		}

		form, err = c.MultipartForm()
		if err != nil {
			log.Printf("POST /feed/:title: get multipart form: %v", err)
			return c.Render("error", fiber.Map{"Error": "Failed Feed Operation", "Reason": "Invalid multipart form"})
		}

		if len(form.Value["method"]) > 0 && form.Value["method"][0] == "remove" {
			_, err = removeFeedQuery.Exec(title)
			if err != nil {
				log.Printf("POST /feed/:title: remove feed %v: %v", title, err)
				return c.Render("error", fiber.Map{"Error": "Failed to Remove Feed", "Reason": "Server error"})
			}

			return c.Render("feedRemove", fiber.Map{"RemovedFeedTitle": title})
		} else {
			if len(form.Value["title"]) == 0 || len(form.Value["description"]) == 0 || len(form.Value["link"]) == 0 {
				return c.Render("error", fiber.Map{"Error": "Failed Updateting Feed", "Reason": "Incomplete form data"})
			}

			_, err = updateFeedQuery.Exec(form.Value["title"][0], form.Value["description"][0], form.Value["link"][0], title)
			if err != nil {
				log.Printf("POST /feed/:title: update feed: %v", err)
				return c.Render("error", fiber.Map{"Error": "Failed Updateting Feed", "Reason": "Server error"})
			}

			// the query rows have to be closed before making further operations on the same table
			var addCat, removeCat []string

			{
				rows, err := feedCategoriesByTitleQuery.Query(title)
				if err != nil {
					log.Printf("POST /feed/:title: get categories: %v", err)
					return c.Render("error", fiber.Map{"Error": "Failed Updateting Feed", "Reason": "Server error"})
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

					isAssignedInForm := false

					for _, cat := range form.Value["category"] {
						if cat == category {
							isAssignedInForm = true
							break
						}
					}

					log.Printf("category: %v, table: %v, form: %v", category, isAssignedInTable, isAssignedInForm)

					if isAssignedInTable && !isAssignedInForm {
						removeCat = append(removeCat, category)
					} else if !isAssignedInTable && isAssignedInForm {
						addCat = append(addCat, category)
					}
				}
			}

			for _, category := range addCat {
				log.Printf("add category %s", category)
				_, err := addFeedCategoryQuery.Exec(title, category)
				if err != nil {
					log.Printf("POST /feed/:title: add category %v: %v", category, err)
				}
			}

			for _, category := range removeCat {
				log.Printf("remove category %s", category)
				_, err := removeFeedCategoryQuery.Exec(title, category)
				if err != nil {
					log.Printf("POST /feed/:title: remove category %v: %v", category, err)
				}
			}

			return c.Render("feedUpdate", fiber.Map{"RemovedFeedTitle": title})
		}
	})

	log.Fatal(app.Listen(":3000"))
}
