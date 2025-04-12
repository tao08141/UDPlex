package main

import (
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Global logger
var logger *zap.SugaredLogger

// initLogger initializes the zap logger based on config
func initLogger(cfg LoggingConfig) {
	// Set defaults if not specified
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	if cfg.Format == "" {
		cfg.Format = "console"
	}
	if cfg.OutputPath == "" {
		cfg.OutputPath = "stdout"
	}

	// Parse log level
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		// Default to info on error
		level = zap.InfoLevel
	}

	// Configure zap
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// Configure encoder based on format
	var encoder zapcore.Encoder
	if strings.ToLower(cfg.Format) == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// Configure output
	var writer zapcore.WriteSyncer
	if strings.ToLower(cfg.OutputPath) == "stdout" {
		writer = zapcore.AddSync(os.Stdout)
	} else {
		file, err := os.OpenFile(cfg.OutputPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			// Fall back to stdout on error
			writer = zapcore.AddSync(os.Stdout)
		} else {
			writer = zapcore.AddSync(file)
		}
	}

	// Create core
	core := zapcore.NewCore(encoder, writer, level)

	// Create logger
	var zapLogger *zap.Logger
	if cfg.Caller {
		zapLogger = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	} else {
		zapLogger = zap.New(core)
	}

	// Create sugared logger for easier use
	logger = zapLogger.Sugar()
}
