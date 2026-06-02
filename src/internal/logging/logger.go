package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

type Level string

const (
	LevelDebug Level = "DEBUG"
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
)

// severity orders levels so the logger can drop messages below its threshold.
func severity(l Level) int {
	switch l {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	default:
		return 1
	}
}

type LogEntry struct {
	Level     Level
	Message   string
	Component string
	TaskID    string
	Timestamp time.Time
}

// Logger writes to file and optional SQLite.
type Logger struct {
	file   io.WriteCloser
	db     *db.Client
	mu     sync.Mutex
	level  Level
	logDir string
}

// New creates a logger that writes to a log file in logDir.
// If db is provided, logs also go to SQLite.
func New(logDir string, dbClient *db.Client, level Level) (*Logger, error) {
	if logDir != "" {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
	}

	logFile := filepath.Join(logDir, "apiary.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	return &Logger{
		file:   f,
		db:     dbClient,
		level:  level,
		logDir: logDir,
	}, nil
}

func (l *Logger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// Info logs at INFO level.
func (l *Logger) Info(ctx context.Context, msg string, component string) {
	l.log(ctx, LevelInfo, msg, component, "")
}

// Warn logs at WARN level.
func (l *Logger) Warn(ctx context.Context, msg string, component string) {
	l.log(ctx, LevelWarn, msg, component, "")
}

// Error logs at ERROR level.
func (l *Logger) Error(ctx context.Context, msg string, component string) {
	l.log(ctx, LevelError, msg, component, "")
}

// Debug logs at DEBUG level.
func (l *Logger) Debug(ctx context.Context, msg string, component string) {
	l.log(ctx, LevelDebug, msg, component, "")
}

// TaskDebug logs a task-specific message at DEBUG level. Used for the verbose
// real-time stream (prompt, claude conversation, routing decision) that only
// shows up when the dispatcher runs with --debug.
func (l *Logger) TaskDebug(ctx context.Context, taskID, msg string) {
	l.log(ctx, LevelDebug, msg, "task", taskID)
}

// TaskInfo logs a task-specific message at INFO level.
func (l *Logger) TaskInfo(ctx context.Context, taskID, msg string) {
	l.log(ctx, LevelInfo, msg, "task", taskID)
}

// TaskError logs a task-specific message at ERROR level.
func (l *Logger) TaskError(ctx context.Context, taskID, msg string) {
	l.log(ctx, LevelError, msg, "task", taskID)
}

func (l *Logger) log(ctx context.Context, level Level, msg, component, taskID string) {
	// Drop messages below the configured threshold (both file and DB).
	if severity(level) < severity(l.level) {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	entry := LogEntry{
		Level:     level,
		Message:   msg,
		Component: component,
		TaskID:    taskID,
		Timestamp: time.Now(),
	}

	// Write to file
	line := fmt.Sprintf("[%s] [%s] %s\n", entry.Timestamp.Format("2006-01-02 15:04:05"), level, msg)
	if component != "" {
		line = fmt.Sprintf("[%s] [%s] [%s] %s\n", entry.Timestamp.Format("2006-01-02 15:04:05"), level, component, msg)
	}
	l.file.Write([]byte(line))

	// Write to database
	if l.db != nil {
		if taskID != "" {
			l.db.WriteTaskLog(ctx, taskID, string(level), msg)
		} else {
			l.db.WriteServiceLog(ctx, string(level), msg, component)
		}
	}
}

// CreateTaskLogger creates a per-task log file in a subdirectory.
func (l *Logger) CreateTaskLogger(taskID string) (io.WriteCloser, error) {
	if l.logDir == "" {
		return os.Stdout, nil
	}

	taskLogDir := filepath.Join(l.logDir, "tasks")
	if err := os.MkdirAll(taskLogDir, 0755); err != nil {
		return nil, fmt.Errorf("create task log dir: %w", err)
	}

	taskLogFile := filepath.Join(taskLogDir, taskID+".log")
	return os.OpenFile(taskLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}
