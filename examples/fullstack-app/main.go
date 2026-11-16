package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/Chidi09/nextgin"
	"github.com/gin-gonic/gin"
)

type Todo struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

type store struct {
	mu     sync.Mutex
	nextID int
	todos  []Todo
}

func newStore() *store {
	return &store{nextID: 3, todos: []Todo{{1, "Serve Next.js from Go", true}, {2, "Write the Gin API", false}}}
}

func (s *store) list() []Todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Todo(nil), s.todos...)
}

func (s *store) add(title string) Todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Todo{ID: s.nextID, Title: title}
	s.nextID++
	s.todos = append(s.todos, t)
	return t
}

func (s *store) setDone(id int, done bool) (Todo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Done = done
			return s.todos[i], true
		}
	}
	return Todo{}, false
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	app, err := nextgin.New("./.nextgin", nextgin.Config{
		Workers: 2,
		Runtime: envOr("NEXTGIN_RUNTIME", "node"), // or "bun"
		Port:    port,
	})
	if err != nil {
		log.Fatal(err)
	}

	todos := newStore()
	api := app.Router.Group("/api")
	api.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	api.GET("/todos", func(c *gin.Context) { c.JSON(http.StatusOK, todos.list()) })
	api.POST("/todos", func(c *gin.Context) {
		var body struct {
			Title string `json:"title" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, todos.add(body.Title))
	})
	api.PATCH("/todos/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		var body struct {
			Done bool `json:"done"`
		}
		if err != nil || c.ShouldBindJSON(&body) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		t, ok := todos.setDone(id, body.Done)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, t)
	})

	// Everything Gin doesn't match: static assets, prerendered pages, and
	// SSR/RSC/Server Actions streamed from the Next.js workers.
	app.MountFrontend()

	if err := app.Run(":" + port); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
