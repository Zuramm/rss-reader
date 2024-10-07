package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mergestat/timediff"
	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"
)

func removeUnordered[Type any](s []Type, i int) []Type {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

func removeOrdered[Type any](slice []Type, s int) []Type {
	return append(slice[:s], slice[s+1:]...)
}

func main() {
	dbg := "main"

	feedParser := gofeed.NewParser()

	policy := bluemonday.UGCPolicy()

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./feeds.db"
	}
	log.Printf("%v: open database %v", dbg, dbPath)
	_, err := os.Stat(dbPath)
	initDb := os.IsNotExist(err)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("%v: open database: %v", dbg, err)
	}
	defer db.Close()

	if initDb {
		log.Printf("%v: creating new database", dbg)
		_, err := db.Exec(`
		CREATE TABLE Feed (
			Title TEXT NOT NULL,
			Link TEXT NOT NULL,
			"Type" INTEGER NOT NULL,
			"Language" TEXT,
			ImageUrl TEXT,
			ImageTitle TEXT, 
			Description TEXT NOT NULL,
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
			log.Fatalf("%v: init database: %v", dbg, err)
		}
	}

	rows := db.QueryRow("PRAGMA user_version;")

	var version int
	err = rows.Scan(&version)

	if err != nil {
		log.Fatalf("%v: get database version: %v", dbg, err)
	}

	newestVersion := 2
	if version > newestVersion {
		log.Fatalf("%v: database version is too high", dbg)
	} else if version != newestVersion {
		log.Printf("%v: upgrading database from version %v to %v", dbg, version, newestVersion)
		tx, err := db.Begin()

		if err != nil {
			log.Fatalf("%v: couldn't start transaction: %v", dbg, err)
		}

		switch version {
		case 0:
			_, err = tx.Exec(`
			ALTER TABLE Feed ADD COLUMN IntervalSeconds INTEGER DEFAULT 3600;
			ALTER TABLE Feed ADD COLUMN DelaySeconds INTEGER DEFAULT 30;
			`)
			if err != nil {
				log.Fatalf("%v: couldn't migrate from version 0: %v", dbg, err)
			}
		case 1:
			_, err = tx.Exec(`
			CREATE TABLE Feed_TEMP (
				Title TEXT NOT NULL,
				Link TEXT NOT NULL,
				"Type" INTEGER NOT NULL,
				"Language" TEXT,
				ImageUrl TEXT,
				ImageTitle TEXT, 
				Description TEXT NOT NULL,
				IntervalSeconds INTEGER DEFAULT 3600,
				DelaySeconds INTEGER DEFAULT 30
			);

			CREATE TABLE FeedCategory_TEMP (
				Feed_FK INTEGER 
					NOT NULL 
					REFERENCES Feed(Title) ON DELETE CASCADE ON UPDATE CASCADE,
				Category TEXT NOT NULL,
				UNIQUE(Feed_FK, Category) ON CONFLICT REPLACE
			);

			CREATE TABLE Post_TEMP (
				GUID TEXT 
					NOT NULL 
					UNIQUE ON CONFLICT IGNORE,
				Title TEXT NOT NULL,
				Link TEXT NOT NULL,
				Content TEXT NOT NULL,
				PublicationDate INTEGER NOT NULL,
				IsRead INTEGER DEFAULT(0) NOT NULL,
				Author TEXT,
				Feed_FK TEXT 
					NOT NULL
					REFERENCES Feed (rowid) ON DELETE CASCADE ON UPDATE CASCADE,
				ImageUrl TEXT,
				Excerpt TEXT
			);

			INSERT INTO Feed_TEMP (rowid, Title, Link, "Type", "Language", ImageUrl, ImageTitle, Description)
				SELECT rowid, Title, Link, "Type", "Language", ImageUrl, ImageTitle, Description FROM Feed;

			INSERT INTO FeedCategory_TEMP (Feed_FK, Category)
				SELECT Feed.rowid AS Feed_FK, Category FROM FeedCategory LEFT JOIN Feed ON FeedCategory.FeedTitle = Feed.Title;

			INSERT INTO Post_TEMP (rowid, GUID, Title, Link, Content, PublicationDate, IsRead, Author, Feed_FK, ImageUrl, Excerpt)
				SELECT Post.rowid, GUID, Post.Title, Post.Link, Content, PublicationDate, IsRead, Author, Feed.rowid AS Feed_FK, Post.ImageUrl, Excerpt 
				FROM Post LEFT JOIN Feed ON Post.FeedTitle = Feed.Title;

			DROP TABLE Feed;

			DROP TABLE FeedCategory;
			
			DROP TABLE Post;

			ALTER TABLE Feed_TEMP RENAME TO Feed;

			ALTER TABLE FeedCategory_TEMP RENAME TO FeedCategory;

			ALTER TABLE Post_TEMP RENAME TO Post;
			`)
			if err != nil {
				log.Fatalf("%v: couldn't migrate from version 1: %v", dbg, err)
			}
		}

		// FIX: Using the ? syntax throws a syntax error
		// _, err = tx.Exec("PRAGMA user_version = ?;", newestVersion)
		_, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d;", newestVersion))

		if err != nil {
			log.Fatalf("%v: update database version: %v", dbg, err)
		}

		err = tx.Commit()

		if err != nil {
			log.Fatalf("%v: transaction failed: %v", dbg, err)
		}
	}

	log.Printf("%v: init fetch queries", dbg)

	log.Printf("%v: spawning fetch threads", dbg)

	pf := NewPostFetcher(feedParser, policy, db)
	pf.spawnThreadsFromDB(db)

	log.Printf("%v: initializing frontend", dbg)
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

	registerPostListEndpoint(db, app)

	registerPostEndpoint(db, app)

	registerFeedListEndpoint(db, app, pf)

	registerFeedEndpoint(db, app, pf)

	log.Fatal(app.Listen(":3000"))
}
