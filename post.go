package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/gofiber/fiber/v2"
)

func registerPostEndpoint(db *sql.DB, app *fiber.App) {
	dbg := "registerPostEndpoint"

	postStmt, err := db.Prepare(`
	SELECT
		Post.Title,
		Post.Link,
		Post.Content,
		Post.PublicationDate,
		Post.Author,
		Feed.rowid,
		Feed.Title,
		Post.ImageUrl,
		Feed.Language
	FROM
		Post
	LEFT JOIN Feed ON Post.Feed_FK = Feed.rowid
	WHERE
		Post.rowid = ?;
	`)
	if err != nil {
		log.Fatalf("%v: prepare post query: %v", dbg, err)
	}

	readPostStmt, err := db.Prepare(`
	UPDATE
		Post
	SET
		IsRead = 1
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("%v: prepare read post query: %v", dbg, err)
	}

	postCategoryStmt, err := db.Prepare(`
	SELECT
		Category
	FROM
		PostCategory
	WHERE
		Post_FK = ?;
	`)
	if err != nil {
		log.Fatalf("%v: prepare post category query: %v", dbg, err)
	}

	app.Get("/post/:id", func(c *fiber.Ctx) error {
		dbg := "GET /post/<id>"

		var rows *sql.Rows
		var row *sql.Row
		var id int
		var err error

		id, err = c.ParamsInt("id")
		if err != nil {
			log.Printf("%v: get title: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Getting Feed",
				"Description": "Invalid title",
			})
		}

		_, err = readPostStmt.Exec(id)
		if err != nil {
			log.Printf("%v: set post as read: %v", dbg, err)
		}

		row = postStmt.QueryRow(id)

		type Post struct {
			Title           string
			Link            string
			Content         string
			PublicationDate int64
			Author          string
			FeedID          int
			FeedTitle       string
			ImageUrl        string
			Language        string
		}

		var post Post

		err = row.Scan(&post.Title, &post.Link, &post.Content, &post.PublicationDate, &post.Author, &post.FeedID, &post.FeedTitle, &post.ImageUrl, &post.Language)
		if err != nil {
			log.Printf("%v: get post data: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title": "Error",
				"Name":  "Failed Getting Post",
			})
		}

		var categories []string

		rows, err = postCategoryStmt.Query(id)
		if err != nil {
			log.Printf("%v: get all categories: %v", dbg, err)
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
				log.Printf("%v: get category data: %v", dbg, err)
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

	postAllDataStmt, err := db.Prepare(`
	SELECT
		Title,
		Link,
		Content,
		ImageUrl,
		Excerpt
	FROM
		Post
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("%v: prepare post all data query: %v", dbg, err)
	}

	updatePostAllDataStmt, err := db.Prepare(`
	UPDATE
		Post
	SET
		Title = ?,
		Content = ?,
		ImageUrl = ?,
		Excerpt = ?
	WHERE
		rowid = ?;
	`)
	if err != nil {
		log.Fatalf("%v: prepare update post all data query: %v", dbg, err)
	}

	// reimport post
	app.Post("/post/:id", func(c *fiber.Ctx) error {
		dbg := "POST /post/<id>"

		var row *sql.Row
		var id int
		var Title, Link, Content, ImageUrl, Excerpt string
		var article readability.Article
		var err error

		id, err = c.ParamsInt("id")
		if err != nil {
			log.Printf("%v: get post id: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Invalid ID",
			})
		}

		// NOTE: A query is neccessary to get the link. The other values help make the query simpler.
		row = postAllDataStmt.QueryRow(id)

		err = row.Scan(&Title, &Link, &Content, &ImageUrl, &Excerpt)
		if err != nil {
			log.Printf("%v: getting all post data: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Couldn't load data",
			})
		}

		// TODO: Use PostFetcher to parse and update the database.
		article, err = ParseArticle(Link, 30*time.Second)
		if err != nil {
			log.Printf("%v: parsing article: %v", dbg, err)
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

		_, err = updatePostAllDataStmt.Exec(Title, Content, ImageUrl, Excerpt, id)
		if err != nil {
			log.Printf("%v: updating post: %v", dbg, err)
			return c.Render("status", fiber.Map{
				"Title":       "Error",
				"Name":        "Failed Reimporting Post",
				"Description": "Couldn't parse article",
			})
		}

		return c.Redirect(fmt.Sprintf("/post/%v", id))
	})
}
