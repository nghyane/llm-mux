package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	defaultLogger *slog.Logger
	logLevel                = new(slog.LevelVar)
	logOutput     io.Writer = os.Stdout
	outputMu      sync.RWMutex
	nowFunc       = time.Now
)

type Fields map[string]any

const (
	DebugLevel = slog.LevelDebug
	InfoLevel  = slog.LevelInfo
	WarnLevel  = slog.LevelWarn
	ErrorLevel = slog.LevelError
)

var initLogger = sync.OnceFunc(func() {
	logLevel.Set(slog.LevelInfo)
	handler := NewCustomHandler(os.Stdout, logLevel, true)
	defaultLogger = slog.New(handler)
})

func init() {
	initLogger()
}

func reconfigureLogger(w io.Writer, addSource bool) {
	outputMu.Lock()
	defer outputMu.Unlock()
	logOutput = w
	handler := NewCustomHandler(w, logLevel, addSource)
	defaultLogger = slog.New(handler)
}

func SetOutput(w io.Writer) {
	reconfigureLogger(w, true)
}

func SetLevel(level slog.Level) {
	logLevel.Set(level)
}

func GetLevel() slog.Level {
	return logLevel.Level()
}

func SetReportCaller(enabled bool) {
	outputMu.RLock()
	w := logOutput
	outputMu.RUnlock()
	reconfigureLogger(w, enabled)
}

func Debug(msg string) {
	logAt(slog.LevelDebug, msg, nil)
}

func Debugf(format string, args ...any) {
	logAt(slog.LevelDebug, fmt.Sprintf(format, args...), nil)
}

func Info(msg string) {
	logAt(slog.LevelInfo, msg, nil)
}

func Infof(format string, args ...any) {
	logAt(slog.LevelInfo, fmt.Sprintf(format, args...), nil)
}

func Warn(msg string) {
	logAt(slog.LevelWarn, msg, nil)
}

func Warnf(format string, args ...any) {
	logAt(slog.LevelWarn, fmt.Sprintf(format, args...), nil)
}

func Error(msg string) {
	logAt(slog.LevelError, msg, nil)
}

func Errorf(format string, args ...any) {
	logAt(slog.LevelError, fmt.Sprintf(format, args...), nil)
}

func Fatal(msg string) {
	logAt(slog.LevelError, msg, nil)
	runExitHandlers()
	os.Exit(1)
}

func Fatalf(format string, args ...any) {
	logAt(slog.LevelError, fmt.Sprintf(format, args...), nil)
	runExitHandlers()
	os.Exit(1)
}

func logAt(level slog.Level, msg string, attrs []slog.Attr) {
	if !defaultLogger.Enabled(context.Background(), level) {
		return
	}

	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])

	r := slog.NewRecord(nowFunc(), level, msg, pcs[0])
	if len(attrs) > 0 {
		r.AddAttrs(attrs...)
	}
	_ = defaultLogger.Handler().Handle(context.Background(), r)
}

type Entry struct {
	attrs []slog.Attr
}

func WithError(err error) *Entry {
	return &Entry{attrs: []slog.Attr{slog.Any("error", err)}}
}

func WithField(key string, value any) *Entry {
	return &Entry{attrs: []slog.Attr{slog.Any(key, value)}}
}

func WithFields(fields Fields) *Entry {
	attrs := make([]slog.Attr, 0, len(fields))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	return &Entry{attrs: attrs}
}

func (e *Entry) WithField(key string, value any) *Entry {
	e.attrs = append(e.attrs, slog.Any(key, value))
	return e
}

func (e *Entry) WithError(err error) *Entry {
	e.attrs = append(e.attrs, slog.Any("error", err))
	return e
}

func (e *Entry) Debug(msg string) {
	e.logAt(slog.LevelDebug, msg)
}

func (e *Entry) Debugf(format string, args ...any) {
	e.logAt(slog.LevelDebug, fmt.Sprintf(format, args...))
}

func (e *Entry) Info(msg string) {
	e.logAt(slog.LevelInfo, msg)
}

func (e *Entry) Infof(format string, args ...any) {
	e.logAt(slog.LevelInfo, fmt.Sprintf(format, args...))
}

func (e *Entry) Warn(msg string) {
	e.logAt(slog.LevelWarn, msg)
}

func (e *Entry) Warnf(format string, args ...any) {
	e.logAt(slog.LevelWarn, fmt.Sprintf(format, args...))
}

func (e *Entry) Error(msg string) {
	e.logAt(slog.LevelError, msg)
}

func (e *Entry) Errorf(format string, args ...any) {
	e.logAt(slog.LevelError, fmt.Sprintf(format, args...))
}

func (e *Entry) logAt(level slog.Level, msg string) {
	if !defaultLogger.Enabled(context.Background(), level) {
		return
	}

	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])

	r := slog.NewRecord(nowFunc(), level, msg, pcs[0])
	r.AddAttrs(e.attrs...)
	_ = defaultLogger.Handler().Handle(context.Background(), r)
}

func Writer() io.Writer {
	return &slogWriter{level: slog.LevelInfo}
}

func WriterLevel(level slog.Level) io.Writer {
	return &slogWriter{level: level}
}

type slogWriter struct {
	level slog.Level
}

func (w *slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	if msg == "" {
		return len(p), nil
	}

	if !defaultLogger.Enabled(context.Background(), w.level) {
		return len(p), nil
	}

	var pcs [1]uintptr
	runtime.Callers(4, pcs[:])

	r := slog.NewRecord(nowFunc(), w.level, msg, pcs[0])
	_ = defaultLogger.Handler().Handle(context.Background(), r)
	return len(p), nil
}

var (
	exitHandlers   []func()
	exitHandlersMu sync.Mutex
)

func RegisterExitHandler(handler func()) {
	exitHandlersMu.Lock()
	defer exitHandlersMu.Unlock()
	exitHandlers = append(exitHandlers, handler)
}

func runExitHandlers() {
	exitHandlersMu.Lock()
	handlers := make([]func(), len(exitHandlers))
	copy(handlers, exitHandlers)
	exitHandlersMu.Unlock()

	for _, h := range handlers {
		h()
	}
}
