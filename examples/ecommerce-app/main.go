package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Chidi09/nextgin"

	"example.com/ecommerce-app/internal/httpapi"
	"example.com/ecommerce-app/internal/service"
	"example.com/ecommerce-app/internal/store"
)

func main() {
	port := envOr("PORT", "8080")
	dbPath, err := filepath.Abs(envOr("SQLITE_PATH", "./store.db"))
	if err != nil {
		log.Fatalf("resolve sqlite path: %v", err)
	}
	webhookSecret := envOr("PAYMENT_WEBHOOK_SECRET", "whsec_local_demo_secret")
	insecureCookies := os.Getenv("COOKIE_INSECURE") != "0"

	repo, err := store.OpenSQLite(dbPath)
	if err != nil {
		log.Fatalf("open sqlite %s: %v", dbPath, err)
	}
	defer repo.Close()

	svc := service.NewStoreService(repo, webhookSecret)
	if err := svc.EnsureDemoUser(context.Background()); err != nil {
		log.Fatalf("seed demo user: %v", err)
	}

	app, err := nextgin.New("./.nextgin", nextgin.Config{
		Workers: 2,
		Runtime: envOr("NEXTGIN_RUNTIME", "node"),
		Port:    port,
		Env:     []string{"SQLITE_PATH=" + dbPath},
	})
	if err != nil {
		log.Fatal(err)
	}

	// 1-minute cron: sweeps abandoned carts older than 15m in SQLite.
	app.Cron("expire-carts", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		if _, err := svc.ExpireAbandonedCarts(r.Context(), 15*time.Minute); err != nil {
			log.Printf("cron expire-carts failed: %v", err)
			nextgin.WriteError(w, http.StatusInternalServerError, "cron job failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	cookieOpts := nextgin.CookieOptions{
		MaxAge:   7 * 86400,
		Insecure: insecureCookies,
	}
	httpapi.NewHandlers(svc, cookieOpts, app.Router).Register(app.Router.Group("/api"))

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
