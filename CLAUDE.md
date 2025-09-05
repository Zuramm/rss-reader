# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

- **Build**: `make` or `go build --tags "fts5" -o rss_reader .`
- **Run**: `make run` or `go run --tags "fts5" .`
- **Clean**: `make clean`
- **Install dependencies**: `make mods` or `go mod download`
- **Nix development**: `nix develop` (provides go, gopls, gotools, go-tools, sqlite, rlwrap)
- **Nix build**: `nix build` (builds the complete package with static assets)

Note: The `fts5` build tag is required for SQLite full-text search functionality.

## Architecture Overview

This is a Go-based RSS reader web application using:

- **Web Framework**: Fiber v2 with HTML templates
- **Database**: SQLite3 with FTS5 for full-text search
- **RSS Parsing**: gofeed library
- **Article Parsing**: go-readability for content extraction
- **HTML Sanitization**: bluemonday (currently disabled but available)

### Core Components

- **main.go**: Application entry point, database initialization, migration logic, and route registration
- **PostFetcher** (fetch-posts.go): Manages concurrent RSS feed fetching with configurable intervals and delays per feed
- **Database Schema**: Three main tables (Feed, Post, FeedCategory, PostCategory) with FTS5 virtual table for search
- **Route Handlers**: 
  - post-list.go: Main page with filtering, search, and pagination
  - feed-list.go: Feed management interface
  - feed.go: Individual feed configuration
  - post.go: Individual post viewing and re-parsing
- **parse-article.go**: Standalone article content extraction utility

### Database Design

- Feeds have configurable fetch intervals and delays
- Posts support categories and full-text search
- Foreign key relationships with cascade delete/update
- Database migration system with version tracking
- Uses prepared statements throughout for SQL injection prevention

### Environment Variables

- `DB_PATH`: Database file location (default: "./feeds.db")
- `VIEWS_PATH`: HTML templates directory (default: "./views") 
- `PORT`: Server port (default: "3000")

### Static Assets

Templates in `views/` directory with layout system, CSS files in `public/` directory.

### Concurrency Model

Each feed runs in its own goroutine with configurable fetch intervals. The PostFetcher manages these threads with a channel-based communication system for starting/stopping feed fetching.