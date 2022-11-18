// Package main defines the example application.
//
// This file is in no way intended to be representative of standard
// golang practices. It is provided merely to run an application that
// uses all the infrastructure defined in the docker configurations to
// demonstrate that everything has been setup and configured properly.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/agschwender/autoreload"
	"github.com/eapache/go-resiliency/retrier"
	"github.com/gomodule/redigo/redis"
	"github.com/lib/pq"
)

const cacheKey string = "example:counter:value"

func openDB(datasource string) (db *sql.DB, err error) {
	log.Printf("Verifying database connection")

	// Retry the connection to the database because if it is started at
	// the same time as the application, the db container may not be
	// accepting connections yet.
	r := retrier.New(retrier.LimitedExponentialBackoff(8, time.Second, 10*time.Second), nil)
	err = r.Run(func() error {
		db, err = sql.Open("postgres", datasource)
		if err != nil {
			return err
		}

		err = db.Ping()
		if err != nil {
			db.Close() // nolint: errcheck
			return err
		}

		return nil
	})

	return
}

func setupDB(datasource string) (*sql.DB, error) {
	if datasource == "" {
		return nil, errors.New("missing db url")
	}

	db, err := openDB(datasource)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()

	query := "CREATE TABLE IF NOT EXISTS example (" +
		"counter INTEGER NOT NULL PRIMARY KEY, " +
		"value INTEGER NOT NULL DEFAULT 0" +
		")"

	_, err = db.ExecContext(ctx, query)
	if err != nil {
		return nil, err
	}

	_, err = db.ExecContext(ctx, "INSERT INTO example (counter, value) VALUES (1, 0)")
	if err != nil {
		pqErr, ok := err.(*pq.Error)
		if !ok || pqErr.Code != pq.ErrorCode("23505") {
			return nil, err
		}
	}

	return db, nil
}

func setupRedis(datasource string) (*redis.Pool, error) {
	if datasource == "" {
		return nil, errors.New("missing redis url")
	}

	return &redis.Pool{
		Dial: func() (redis.Conn, error) {
			return redis.DialURL(datasource)
		},
	}, nil
}

func incr(db *sql.DB, cache *redis.Pool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		log.Printf("Serving incr")

		ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
		defer cancel()

		var err error
		defer func() {
			if err != nil {
				writeError(w, err)
			}
		}()

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return
		}
		defer tx.Rollback() // nolint: errcheck

		_, err = tx.ExecContext(ctx, "UPDATE example SET value = value + 1 WHERE counter = 1")
		if err != nil {
			return
		}

		query := "SELECT value FROM example WHERE counter = 1"
		var value int
		err = tx.QueryRowContext(ctx, query).Scan(&value)
		if err != nil {
			return
		}

		cacheConn, err := cache.GetContext(ctx)
		if err != nil {
			log.Printf("Unable to connect to cache: %v", err)
		} else {
			defer cacheConn.Close()
			_, err = redis.DoContext(cacheConn, ctx, "SET", cacheKey, value)
			if err != nil {
				log.Printf("Unable to set cache value: %v", err)
			}
		}

		err = tx.Commit()
		if err != nil {
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"value": value}) // nolint: errcheck
	}
}

func view(db *sql.DB, cache *redis.Pool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		log.Printf("Serving view")

		ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
		defer cancel()

		var err error
		defer func() {
			if err != nil {
				writeError(w, err)
			}
		}()

		var value int
		var hasValue bool
		cacheConn, err := cache.GetContext(ctx)
		if err != nil {
			log.Printf("Unable to connect to cache: %v", err)
		} else {
			defer cacheConn.Close()
			value, err = redis.Int(redis.DoContext(cacheConn, ctx, "GET", cacheKey))
			if err != nil {
				if err == redis.ErrNil {
					// Ignore not found error
					err = nil
				} else {
					log.Printf("Unable to get cache value: %v", err)
				}
			} else {
				hasValue = true
			}
		}

		if !hasValue {
			query := "SELECT value FROM example WHERE counter = 1"
			err = db.QueryRowContext(ctx, query).Scan(&value)
			if err != nil {
				return
			}
			log.Printf("Got value from db")
		} else {
			log.Printf("Got value from cache")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"value": value}) // nolint: errcheck
	}
}

func ping(w http.ResponseWriter, req *http.Request) {
	log.Printf("Serving ping")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ping": "pong"}) // nolint: errcheck
}

func shutdown(server *http.Server) {
	log.Printf("Shutting down server")
	err := server.Shutdown(context.Background())
	if err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Print("Shutting down application")
}

func writeError(w http.ResponseWriter, err error) {
	log.Printf("Request failed: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) // nolint: errcheck
}

func main() {
	log.Printf("Starting application")

	db, err := setupDB(os.Getenv("DB_URL"))
	if err != nil {
		log.Fatalf("Failed to start DB: %v", err)
	}
	defer db.Close()

	cache, err := setupRedis(os.Getenv("REDIS_URL"))
	if err != nil {
		log.Fatalf("Failed to start redis client: %v", err)
	}
	defer cache.Close()

	http.HandleFunc("/ping", ping)
	http.HandleFunc("/incr", incr(db, cache))
	http.HandleFunc("/view", view(db, cache))

	server := &http.Server{
		Addr: ":8080",
	}

	shouldAutoReload, _ := strconv.ParseBool(os.Getenv("AUTORELOAD")) // nolint: errcheck
	if shouldAutoReload {
		autoReloader := autoreload.New(
			autoreload.WithMaxAttempts(6),
			autoreload.WithOnReload(func() {
				shutdown(server)
			}),
		)

		autoReloader.Start()
	}

	go func() {
		log.Printf("Starting HTTP server")
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt, syscall.SIGTERM)
	<-interruptChannel

	log.Printf("Received interrupt signal, shutting down")
	shutdown(server)
}
