package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/gofiber/fiber/v2"
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

func convertArgs(args []string) []interface{} {
	ifaces := make([]interface{}, len(args))
	for i, v := range args {
		ifaces[i] = v
	}
	return ifaces
}

func registerPostListEndpoint(db *sql.DB, app *fiber.App) {
	allFeedsTitle, err := db.Prepare(`
	SELECT
		rowid,
		Title
	FROM
		Feed
	ORDER BY
		Title ASC;
	`)
	if err != nil {
		log.Fatalf("main: prepare all feeds title: %v", err)
	}

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

	allPostQueryStr := `
	SELECT
		Post.rowid,
		Post.Title,
		Post.Excerpt,
		Post.PublicationDate,
		Post.IsRead,
		Post.Author,
		Feed.rowid,
		Feed.Title,
		Post.ImageUrl,
		Feed.Language
	FROM
		Post
	LEFT JOIN Feed ON Post.Feed_FK = Feed.rowid
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
		FeedTitle IN (%s)
	`

	allPostQueryFeedCategoryStr := `
	Feed_FK IN (
		SELECT
			Feed_FK FROM FeedCategory
		WHERE
			Category IN(%s)
	)
	`

	allPostQueryPostCategoryStr := `
	rowid IN (
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
			log.Printf("GET /: get all feeds: %v", err)
		} else {
			for rows.Next() {
				var id int64
				var feed string
				err := rows.Scan(&id, &feed)
				if err != nil {
					log.Printf("GET /: get feed data: %v", err)
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
			log.Printf("GET /: get all feed categories: %v", err)
		} else {
			for rows.Next() {
				var category string
				err := rows.Scan(&category)
				if err != nil {
					log.Printf("GET /: get feed category data: %v", err)
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
			log.Printf("GET /: get all post categories: %v", err)
		} else {
			for rows.Next() {
				var category string
				err := rows.Scan(&category)
				if err != nil {
					log.Printf("GET /: get post category data: %v", err)
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

		type Post struct {
			Rowid           string
			Title           string
			Excerpt         string
			PublicationDate int64
			IsRead          bool
			Author          string
			FeedID          int
			FeedTitle       string
			ImageUrl        string
			Language        string
		}

		var posts []Post

		for rows.Next() {
			var post Post
			err := rows.Scan(&post.Rowid, &post.Title, &post.Excerpt, &post.PublicationDate, &post.IsRead, &post.Author, &post.FeedID, &post.FeedTitle, &post.ImageUrl, &post.Language)
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
}
