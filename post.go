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
	postStmt, err := db.Prepare(`
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

	readPostStmt, err := db.Prepare(`
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

	postCategoryStmt, err := db.Prepare(`
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

		_, err = readPostStmt.Exec(id)
		if err != nil {
			log.Printf("GET /post/:id: set post as read: %v", err)
		}

		row = postStmt.QueryRow(id)

		type Post struct {
			Title           string
			Link            string
			Content         string
			PublicationDate int64
			Author          string
			FeedTitle       string
			ImageUrl        string
			Language        string
		}

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

		rows, err = postCategoryStmt.Query(id)
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

	postAllDataStmt, err := db.Prepare(`
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

	updatePostAllDataStmt, err := db.Prepare(`
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

		row = postAllDataStmt.QueryRow(id)

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

		_, err = updatePostAllDataStmt.Exec(GUID, Title, Link, Content, PublicationDate, IsRead, Author, FeedTitle, ImageUrl, Excerpt, id)
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
}
