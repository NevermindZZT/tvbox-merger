package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"tvbox-merger/internal/config"
	"tvbox-merger/internal/database"
	"tvbox-merger/internal/handler"
)

func main() {
	// Load .env file (if exists)
	_ = godotenv.Load()

	cfg := config.Load()

	// Init database
	db, err := database.Init(cfg)
	if err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}

	// Ensure default group exists
	database.EnsureDefaultGroup(db)

	// Init cache dir
	if err := database.EnsureCacheDir(cfg.CacheDir); err != nil {
		log.Fatalf("Failed to init cache dir: %v", err)
	}

	// Create Gin engine
	r := gin.Default()

	// Register custom template functions
	r.SetFuncMap(template.FuncMap{
		"json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	})

	// Serve static files
	r.Static("/static", "./web/static")

	// Load templates
	r.LoadHTMLGlob("web/templates/*")

	// Setup routes (returns scheduler)
	sched := handler.SetupRoutes(r, db, cfg)

	// Start scheduler
	sched.Start()
	defer sched.Stop()

	log.Printf("TVBox Merger started on port %s", cfg.Port)
	if err := r.Run(fmt.Sprintf(":%s", cfg.Port)); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
