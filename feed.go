package main

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

func registerFeedEndpoint(db *sql.DB, app *fiber.App, pf *PostFetcher) {
	feedStmt, err := db.Prepare(`
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
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare feed query: %v", err)
	}

	feedCategoriesByTitleStmt, err := db.Prepare(`
	SELECT DISTINCT
		Category,
		CASE WHEN EXISTS (
			SELECT
				1
			FROM
				FeedCategory
			WHERE
				Category = t.Category
			AND Feed_FK = ?)
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

	feedLanguagesStmt, err := db.Prepare(`
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

	app.Get("/feed/:id", func(c *fiber.Ctx) error {
		id, err := c.ParamsInt("id")
		if err != nil {
			log.Printf("GET /feed/:id: get id: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": "Invalid id",
			})
		}

		row := feedStmt.QueryRow(id)

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
			log.Printf("GET /feed/:id: scan feed row: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": fmt.Sprintf("Couldn't load data for \"%s\"", id),
			})
		}

		feed.Interval = (time.Duration(intervalSeconds) * time.Second).String()
		feed.Delay = (time.Duration(delaySeconds) * time.Second).String()

		rows, err := feedCategoriesByTitleStmt.Query(id)
		if err != nil {
			log.Printf("GET /feed/:id: get feed categories: %v", err)
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
				log.Printf("GET /feed/:id: scan feed category data: %v", err)
				continue
			}

			categories = append(categories, value)
		}

		rows, err = feedLanguagesStmt.Query()
		if err != nil {
			log.Printf("GET /feed/:id: get feed categories: %v", err)
		}

		var languageSuggestions []string

		if rows != nil {
			for rows.Next() {
				var value string
				err = rows.Scan(&value)
				if err != nil {
					log.Printf("GET /feed/:id: scan feed languages: %v", err)
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

	removeFeedStmt, err := db.Prepare(`
	DELETE FROM
		Feed
	WHERE 
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare remove feed query: %v", err)
	}

	updateFeedStmt, err := db.Prepare(`
	UPDATE
		Feed
	SET
		Title = ?,
		Description = ?,
		"Link" = ?,
		IntervalSeconds = ?,
		DelaySeconds = ?
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare update feed query: %v", err)
	}

	addFeedCategoryStmt, err := db.Prepare(`
	INSERT INTO 
		FeedCategory(Feed_FK, Category)
	VALUES
		            (?      , ?       )
	`)
	if err != nil {
		log.Fatalf("main: prepare add feed category query: %v", err)
	}

	removeFeedCategoryStmt, err := db.Prepare(`
	DELETE FROM
		FeedCategory
	WHERE
		Feed_FK = ?
		AND Category = ?;
	`)
	if err != nil {
		log.Fatalf("main: prepare remove feed category query: %v", err)
	}

	app.Post("/feed/:id", func(c *fiber.Ctx) error {
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil {
			log.Printf("POST /feed/:id: get id from param: %v", err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Feed Operation",
				"Description": "Invalid feed id",
			})
		}

		form, err := c.MultipartForm()
		if err != nil {
			log.Printf("POST /feed/:id: get multipart form: %v", err)
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
			_, err = removeFeedStmt.Exec(id)
			if err != nil {
				log.Printf("POST /feed/:id: remove feed %v: %v", id, err)
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed to Remove Feed",
					"Description": "Server error",
				})
			}

			pf.KillThread(id)

			return c.Render("status", fiber.Map{
				"Title":       "Deleted Feed",
				"Name":        "Deleted Feed Successfully",
				"Description": fmt.Sprintf("Deleted feed with id %v", id),
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

			_, err = updateFeedStmt.Exec(form.Value["title"][0], form.Value["description"][0], form.Value["link"][0], interval.Seconds(), delay.Seconds(), id)
			if err != nil {
				log.Printf("POST /feed/:id: update feed: %v", err)
				return c.Render("status", fiber.Map{
					"Title":       "Error",
					"Name":        "Failed Updateting Feed",
					"Description": "Server error",
				})
			}

			pf.KillThread(id)
			pf.spawnThread(id, form.Value["link"][0], interval, delay)

			// the query rows have to be closed before making further operations on the same table
			var addCat, removeCat []string

			addCat = form.Value["category"]

			log.Print("get previous categories")
			{
				rows, err := feedCategoriesByTitleStmt.Query(id)
				if err != nil {
					log.Printf("POST /feed/:id: get categories: %v", err)
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
						log.Printf("POST /feed/:id: read category data: %v", err)
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
						addCat[categoryIndex] = addCat[len(addCat)-1]
						addCat = addCat[:len(addCat)-1]
					}
				}
			}

			log.Print("update categories")
			for _, category := range addCat {
				if category == "" {
					continue
				}
				_, err := addFeedCategoryStmt.Exec(id, category)
				if err != nil {
					log.Printf("POST /feed/:title: add category %v: %v", category, err)
				}
			}

			for _, category := range removeCat {
				_, err := removeFeedCategoryStmt.Exec(id, category)
				if err != nil {
					log.Printf("POST /feed/:title: remove category %v: %v", category, err)
				}
			}

			log.Print("render response")
			return c.Render("status", fiber.Map{
				"Title":       "Updated Feed",
				"Name":        "Updated Feed Successfully",
				"Description": fmt.Sprintf("Updated feed with title %v", id),
			})
		}
	})
}
