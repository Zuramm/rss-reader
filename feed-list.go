package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
)

func registerFeedListEndpoint(db *sql.DB, app *fiber.App, pf *PostFetcher) {
	allFeedsStmt, err := db.Prepare(`
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

	app.Get("/feed", func(c *fiber.Ctx) error {
		rows, err := allFeedsStmt.Query()
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

	newFeedStmt, err := db.Prepare(`
	INSERT INTO Feed(Title, Description, Link, Type, Language, ImageUrl, ImageTitle)
	    VALUES      (?,     ?,        ?,       ?,    ?,        ?,        ?         )
	`)
	if err != nil {
		log.Fatalf("main: prepare new feed query: %v", err)
	}

	app.Post("/feed", func(c *fiber.Ctx) error {
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

		_, err = newFeedStmt.Exec(feed.Title, feed.Description, feedLink, 0, feed.Language, "", "") // feed.Image.URL, feed.Image.Title)
		if err != nil {
			log.Printf("POST /feed: add feed: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed to Create Feed",
				"Description": "Failed database query",
			})
		}

		pf.spawnThread(rssUrl, 3600*time.Second, 30*time.Second)

		return c.Render("status", fiber.Map{
			"Title":       "Added Feed",
			"Name":        "Added Feed Successfully",
			"Description": fmt.Sprintf("Added feed with title %v", feed.Title),
		})
	})

}
