package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	writerMu       sync.Mutex
	logWriter      *lumberjack.Logger
	ginInfoWriter  io.Writer
	ginErrorWriter io.Writer
)

var setupBaseLogger = sync.OnceFunc(func() {
	SetOutput(os.Stdout)
	SetLevel(slog.LevelInfo)
	SetReportCaller(true)

	gin.SetMode(gin.ReleaseMode)

	ginInfoWriter = Writer()
	gin.DefaultWriter = ginInfoWriter
	ginErrorWriter = WriterLevel(slog.LevelError)
	gin.DefaultErrorWriter = ginErrorWriter
	gin.DebugPrintFunc = func(format string, values ...any) {
		Debugf(format, values...)
	}

	RegisterExitHandler(closeLogOutputs)
})

func SetupBaseLogger() {
	setupBaseLogger()
}

func ConfigureLogOutput(loggingToFile bool) error {
	SetupBaseLogger()

	writerMu.Lock()
	defer writerMu.Unlock()

	if loggingToFile {
		logDir := "logs"
		if base := writablePath(); base != "" {
			logDir = filepath.Join(base, "logs")
		}
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("logging: failed to create log directory: %w", err)
		}
		if logWriter != nil {
			_ = logWriter.Close()
		}
		logWriter = &lumberjack.Logger{
			Filename:   filepath.Join(logDir, "main.log"),
			MaxSize:    10,
			MaxBackups: 0,
			MaxAge:     0,
			Compress:   false,
		}
		SetOutput(logWriter)
		return nil
	}

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	SetOutput(os.Stdout)
	return nil
}

func closeLogOutputs() {
	writerMu.Lock()
	defer writerMu.Unlock()

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
}
