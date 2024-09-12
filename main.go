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

	rows := db.QueryRow("PRAGMA user_version;")

	var version int
	err = rows.Scan(&version)

	if err != nil {
		log.Fatalf("main: get database version: %v", err)
	}

	newestVersion := 1
	if version > newestVersion {
		log.Fatalf("main: database version is too high")
	} else if version != newestVersion {
		log.Printf("upgrading database from version %v to %v", version, newestVersion)
		tx, err := db.Begin()

		if err != nil {
			log.Fatalf("main: couldn't start transaction: %v", err)
		}

		switch version {
		case 0:
			tx.Exec("ALTER TABLE Feed ADD COLUMN IntervalSeconds INTEGER DEFAULT 3600")
			tx.Exec("ALTER TABLE Feed ADD COLUMN DelaySeconds INTEGER DEFAULT 30")
		}

		err = tx.Commit()

		if err != nil {
			log.Fatalf("main: transaction failed: %v", err)
		}

		_, err = db.Exec("PRAGMA user_version = ?;", newestVersion)

		if err != nil {
			log.Fatalf("main: update database version: %v", err)
		}
	}

	log.Printf("init fetch queries")

	log.Printf("spawning fetch threads")

	pf := NewPostFetcher(feedParser, policy, db)
	pf.spawnThreadsFromDB(db)

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

	registerPostListEndpoint(db, app)

	registerPostEndpoint(db, app)

	registerFeedListEndpoint(db, app, pf)

	registerFeedEndpoint(db, app, pf)

	log.Fatal(app.Listen(":3000"))
}
