package utils

import (
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	logger    *zap.SugaredLogger
	once      sync.Once
	debugMode bool
	debugFile *os.File
)

// InitLogger initializes the global zap logger
func InitLogger(level string, enableDebugFile bool) {
	once.Do(func() {
		debugMode = enableDebugFile

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

		var cores []zapcore.Core

		// Console output for info and above (excludes debug)
		consoleLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= zapcore.InfoLevel && atomicLevel.Enabled(lvl)
		})
		consoleCore := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(os.Stdout),
			consoleLevel,
		)
		cores = append(cores, consoleCore)

		// If debug mode is enabled, write debug logs to server.log
		if debugMode && atomicLevel.Level() == zap.DebugLevel {
			var err error
			debugFile, err = os.OpenFile("server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				// Fallback: if we can't open the file, just log to console
				os.Stderr.WriteString("Warning: Could not open server.log for debug logging: " + err.Error() + "\n")
			} else {
				debugLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
					return lvl == zapcore.DebugLevel
				})
				debugCore := zapcore.NewCore(
					zapcore.NewConsoleEncoder(encoderConfig),
					zapcore.AddSync(debugFile),
					debugLevel,
				)
				cores = append(cores, debugCore)
			}
		}

		core := zapcore.NewTee(cores...)
		l := zap.New(core, zap.AddCaller())
		logger = l.Sugar()
	})
}

// L returns the global sugared logger
func L() *zap.SugaredLogger {
	if logger == nil {
		// Fallback to a basic logger if InitLogger wasn't called
		InitLogger("info", false)
	}
	return logger
}

// CloseLogger closes the debug log file if it was opened
func CloseLogger() {
	if debugFile != nil {
		debugFile.Close()
	}
}
