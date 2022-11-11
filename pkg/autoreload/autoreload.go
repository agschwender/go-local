// Package autoreload restarts a process if its executable changes.
package autoreload

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

const defaultMaxAttempts = 10

type onReloadFunc func()

type Logger interface {
	Info(string)
	Fatal(string, error)
}

type defaultLogger struct{}

func (l *defaultLogger) Info(msg string) {
	log.Println(msg)
}

func (l *defaultLogger) Fatal(msg string, err error) {
	log.Fatalf("%s: %v\n", msg, err)
}

type AutoReloader struct {
	logger      Logger
	maxAttempts int
	onReload    onReloadFunc

	ctx    context.Context
	cancel context.CancelFunc
}

type option func(*AutoReloader)

func New(opts ...option) AutoReloader {
	ctx, cancel := context.WithCancel(context.TODO())
	autoReloader := &AutoReloader{
		logger:      &defaultLogger{},
		maxAttempts: defaultMaxAttempts,
		onReload:    func() {},
		ctx:         ctx,
		cancel:      cancel,
	}
	for _, opt := range opts {
		opt(autoReloader)
	}
	return *autoReloader
}

func WithLogger(logger Logger) option {
	return func(autoReloader *AutoReloader) {
		autoReloader.logger = logger
	}
}

func WithMaxAttempts(maxAttempts int) option {
	return func(autoReloader *AutoReloader) {
		autoReloader.maxAttempts = maxAttempts
	}
}

func WithOnReload(onReload onReloadFunc) option {
	return func(autoReloader *AutoReloader) {
		autoReloader.onReload = onReload
	}
}

// Start launches a goroutine that periodically checks if the modified
// time of the binary has changed. If so, the binary is re-executed with
// the same arguments. This is a developer convenience and not intended
// to be started in the production environment.
func (ar AutoReloader) Start() {
	execPath := mustLookPath(ar.logger, os.Args[0])

	watcher, err := fsnotify.NewWatcher()
	must(ar.logger, err, "Failed to create file watcher")
	must(ar.logger, watcher.Add(execPath), "Failed to watch file")

	go func() {
		for {
			select {
			case <-watcher.Events:
				ar.logger.Info("Executable changed; reloading process")
				for i := 0; i < ar.maxAttempts; i++ {
					sleep(250*time.Millisecond, watcher.Events)
					ar.onReload()
					tryExec(ar.logger, execPath, os.Args, os.Environ())
				}
				ar.logger.Fatal("Failed to reload process", nil)
			case err := <-watcher.Errors:
				must(ar.logger, err, "Error watching file")
			case <-ar.ctx.Done():
				return
			}
		}
	}()
}

// Stop will stop the autoreloader from watching the executable and reloading it.
func (ar AutoReloader) Stop() {
	ar.cancel()
}

func must(logger Logger, err error, msg string) {
	if err != nil {
		logger.Fatal(msg, err)
	}
}

func mustLookPath(logger Logger, name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		logger.Fatal(fmt.Sprintf("Cannot find executable: %s", name), err)
	}
	return path
}

// sleep pauses the current goroutine for at least duration d, swallowing
// all fsnotify events received in the interim.
func sleep(d time.Duration, events chan fsnotify.Event) { // nolint: unparam
	timer := time.After(d)
	for {
		select {
		case <-events:
		case <-timer:
			return
		}
	}
}

func tryExec(logger Logger, argv0 string, argv []string, envv []string) {
	if err := syscall.Exec(argv0, argv, envv); err != nil {
		if errno, ok := err.(syscall.Errno); ok {
			if errno == syscall.ETXTBSY {
				return
			}
		}
		logger.Fatal(fmt.Sprintf("syscall.Exec: %s", argv0), err)
	}
	os.Exit(0)
}
