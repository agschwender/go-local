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
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/agschwender/autoreload"
	"github.com/agschwender/errcat-go"
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

func setupDB(errcatD *errcat.Daemon, datasource string) (*sql.DB, error) {
	if datasource == "" {
		return nil, errors.New("missing db url")
	}

	log.Printf("setting up postgres")

	key, _ := errcatD.RegisterCaller(errcat.New("postgres", "openDB"))

	var db *sql.DB
	var err error
	err = errcatD.Call(key, func() error {
		db, err = openDB(datasource)
		return err
	})

	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()

	query := "CREATE TABLE IF NOT EXISTS example (" +
		"counter INTEGER NOT NULL PRIMARY KEY, " +
		"value INTEGER NOT NULL DEFAULT 0" +
		")"

	key, _ = errcatD.RegisterCaller(errcat.New("postgres", "createTableExample"))
	err = errcatD.Call(key, func() error {
		_, err := db.ExecContext(ctx, query)
		return err
	})
	if err != nil {
		return nil, err
	}

	key, _ = errcatD.RegisterCaller(errcat.New("postgres", "insertCounterIntoExample"))
	err = errcatD.Call(key, func() error {
		_, err := db.ExecContext(ctx, "INSERT INTO example (counter, value) VALUES (1, 0)")
		return err
	})
	if err != nil {
		pqErr, ok := err.(*pq.Error)
		if !ok || pqErr.Code != pq.ErrorCode("23505") {
			return nil, err
		}
	}

	return db, nil
}

func setupErrcat(addr string) *errcat.Daemon {
	if addr == "" {
		log.Printf("errcat address is empty")
		return nil
	}

	errcatAddr, err := url.Parse(addr)
	if err != nil {
		log.Printf("Invalid errcat URL: %s", addr)
		return nil
	}

	log.Printf("errcat address = %s", errcatAddr.String())

	return errcat.NewD(
		errcat.WithEnvironment("local"),
		errcat.WithServerAddr(*errcatAddr),
		errcat.WithService("go-local"),
	)
}

func setupRedis(errcatD *errcat.Daemon, datasource string) (*redis.Pool, error) {
	if datasource == "" {
		return nil, errors.New("missing redis url")
	}

	log.Printf("setting up redis")

	key, _ := errcatD.RegisterCaller(errcat.New("redis", "dial"))
	return &redis.Pool{
		Dial: func() (redis.Conn, error) {
			var conn redis.Conn
			err := errcatD.Call(key, func() error {
				var connErr error
				conn, connErr = redis.DialURL(datasource)
				return connErr
			})
			return conn, err
		},
	}, nil
}

func incr(db *sql.DB, cache *redis.Pool, errcatD *errcat.Daemon) func(http.ResponseWriter, *http.Request) {
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

		key, _ := errcatD.RegisterCaller(errcat.New("postgres", "updateCounter"))
		err = errcatD.Call(key, func() error {
			_, err = tx.ExecContext(ctx, "UPDATE example SET value = value + 1 WHERE counter = 1")
			return err
		})
		if err != nil {
			return
		}

		key, _ = errcatD.RegisterCaller(errcat.New("postgres", "getCounter"))
		var value int
		err = errcatD.Call(key, func() error {
			query := "SELECT value FROM example WHERE counter = 1"
			return tx.QueryRowContext(ctx, query).Scan(&value)
		})
		if err != nil {
			return
		}

		cacheConn, err := cache.GetContext(ctx)
		if err != nil {
			log.Printf("Unable to connect to cache: %v", err)
		} else {
			defer cacheConn.Close()
			key, _ = errcatD.RegisterCaller(errcat.New("redis", "setCounter"))
			err = errcatD.Call(key, func() error {
				_, err = redis.DoContext(cacheConn, ctx, "SET", cacheKey, value)
				return err
			})
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

func view(db *sql.DB, cache *redis.Pool, errcatD *errcat.Daemon) func(http.ResponseWriter, *http.Request) {
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

			key, _ := errcatD.RegisterCaller(errcat.New("redis", "getCounter"))
			err = errcatD.Call(key, func() error {
				value, err = redis.Int(redis.DoContext(cacheConn, ctx, "GET", cacheKey))
				if err != nil {
					if err != redis.ErrNil {
						// Ignore not found error
						return err
					}
				} else {
					hasValue = true
				}
				return nil
			})
			if err != nil {
				log.Printf("Unable to get cache value: %v", err)
			}
		}

		if !hasValue {
			key, _ := errcatD.RegisterCaller(errcat.New("postgres", "getCounter"))
			err = errcatD.Call(key, func() error {
				query := "SELECT value FROM example WHERE counter = 1"
				return db.QueryRowContext(ctx, query).Scan(&value)
			})
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

	errcatD := setupErrcat(os.Getenv("ERRCAT_URL"))
	errcatD.Start()
	defer errcatD.Stop()

	db, err := setupDB(errcatD, os.Getenv("DB_URL"))
	if err != nil {
		log.Fatalf("Failed to start DB: %v", err)
	}
	defer db.Close()

	cache, err := setupRedis(errcatD, os.Getenv("REDIS_URL"))
	if err != nil {
		log.Fatalf("Failed to start redis client: %v", err)
	}
	defer cache.Close()

	http.HandleFunc("/ping", ping)
	http.HandleFunc("/incr", incr(db, cache, errcatD))
	http.HandleFunc("/view", view(db, cache, errcatD))

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
