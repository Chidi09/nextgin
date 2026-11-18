package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Chidi09/nextgin"
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
	return &store{nextID: 3, todos: []Todo{{1, "Serve Next.js from Go", true}, {2, "Write the Go API", false}}}
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
	var heartbeats atomic.Int64
	app.Cron("heartbeat", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		heartbeats.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})

	api := app.Router.Group("/api")
	api.GET("/health", func(w http.ResponseWriter, r *http.Request) {
		nextgin.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	api.GET("/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		nextgin.WriteJSON(w, http.StatusOK, map[string]int64{"count": heartbeats.Load()})
	})
	api.GET("/todos", func(w http.ResponseWriter, r *http.Request) {
		nextgin.WriteJSON(w, http.StatusOK, todos.list())
	})
	api.POST("/todos", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string `json:"title"`
		}
		if err := nextgin.ReadJSON(r, &body); err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(body.Title) == "" {
			nextgin.WriteError(w, http.StatusBadRequest, "title is required")
			return
		}
		nextgin.WriteJSON(w, http.StatusCreated, todos.add(body.Title))
	})
	api.PATCH("/todos/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		var body struct {
			Done bool `json:"done"`
		}
		if err != nil || nextgin.ReadJSON(r, &body) != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "bad request")
			return
		}
		t, ok := todos.setDone(id, body.Done)
		if !ok {
			nextgin.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, t)
	})

	// Everything Router doesn't match: static assets, prerendered pages, and
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
