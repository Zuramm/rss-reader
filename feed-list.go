package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
)

func registerFeedListEndpoint(db *sql.DB, app *fiber.App, pf *PostFetcher) {
	dbg := "registerFeedListEndpoint"

	allFeedsStmt, err := db.Prepare(`
	SELECT
		rowid,
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
		log.Fatalf("%v: prepare all feeds query: %v", dbg, err)
	}

	app.Get("/feed", func(c *fiber.Ctx) error {
		dbg := "GET /feed"

		rows, err := allFeedsStmt.Query()
		if err != nil {
			log.Printf("%v: get all feeds: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title": "Error",
				"Name":  "Failed Loading Feeds",
			})
		}
		defer rows.Close()

		type Feed struct {
			ID          int
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
			err := rows.Scan(&feed.ID, &feed.Title, &feed.Description, &feed.Link, &feed.Language, &feed.ImageUrl, &feed.ImageTitle)
			if err != nil {
				log.Printf("%v: get feed data: %v", dbg, err)
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

	newFeedStmt, err := db.Prepare(`
	INSERT INTO 
		Feed(Title, Description, Link, Type, Language, ImageUrl, ImageTitle)
	VALUES
		    (?,     ?,        ?,       ?,    ?,        ?,        ?         );
	`)
	if err != nil {
		log.Fatalf("%v: prepare new feed query: %v", dbg, err)
	}

	app.Post("/feed", func(c *fiber.Ctx) error {
		dbg := "POST /feed"

		rssUrl := c.FormValue("url")
		if rssUrl == "" {
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed to Create Feed",
				"Description": "Missing RSS-URL",
			})
		}

		feed, err := pf.feedParser.ParseURL(rssUrl)
		if err != nil {
			log.Printf("%v: parse feed: %v", dbg, err)
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

		res, err := newFeedStmt.Exec(feed.Title, feed.Description, feedLink, 0, feed.Language, "", "") // feed.Image.URL, feed.Image.Title)
		if err != nil {
			log.Printf("%v: add feed: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed to Create Feed",
				"Description": "Failed database query",
			})
		}

		id, err := res.LastInsertId()
		if err != nil {
			log.Printf("%v: get new id: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed to Create Feed",
				"Description": "Failed database query",
			})
		}

		pf.spawnThread(id, rssUrl, 3600*time.Second, 30*time.Second)

		return c.Render("status", fiber.Map{
			"Title":       "Added Feed",
			"Name":        "Added Feed Successfully",
			"Description": fmt.Sprintf("Added feed with title %v", feed.Title),
		})
	})

}
