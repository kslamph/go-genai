package utils

import (
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	logger *zap.SugaredLogger
	once   sync.Once
)

// InitLogger initializes the global zap logger
func InitLogger(level string) {
	once.Do(func() {
		atomicLevel := zap.NewAtomicLevel()
		switch level {
		case "debug":
			atomicLevel.SetLevel(zap.DebugLevel)
		case "info":
			atomicLevel.SetLevel(zap.InfoLevel)
		case "warn":
			atomicLevel.SetLevel(zap.WarnLevel)
		case "error":
			atomicLevel.SetLevel(zap.ErrorLevel)
		default:
			atomicLevel.SetLevel(zap.InfoLevel)
		}

		encoderConfig := zap.NewProductionEncoderConfig()
		encoderConfig.TimeKey = "timestamp"
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

		// Console output for all logs
		consoleCore := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(os.Stdout),
			atomicLevel,
		)

		core := zapcore.NewTee(consoleCore)
		l := zap.New(core, zap.AddCaller())
		logger = l.Sugar()
	})
}

// L returns the global sugared logger
func L() *zap.SugaredLogger {
	if logger == nil {
		// Fallback to a basic logger if InitLogger wasn't called
		InitLogger("info")
	}
	return logger
}

// CloseLogger closes any open resources
func CloseLogger() {
	// No-op since we removed debug file logging
}
