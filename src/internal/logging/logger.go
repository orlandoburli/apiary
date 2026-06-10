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

// Rotation controls size-based rotation of the shared apiary.log file and
// age-based pruning of rotated backups and per-task log files. Values <= 0
// disable the corresponding behaviour.
type Rotation struct {
	MaxSizeMB  int // rotate apiary.log past this size; <=0 disables rotation
	MaxBackups int // rotated files to keep (apiary.log.1 .. .N); <=0 keeps none
	MaxAgeDays int // delete backups and task logs older than this; <=0 disables
}

// DefaultRotation mirrors the defaults config.Load applies when the settings
// are absent from apiary.yaml.
func DefaultRotation() Rotation {
	return Rotation{MaxSizeMB: 50, MaxBackups: 5, MaxAgeDays: 30}
}

// Logger writes to file and optional SQLite.
type Logger struct {
	file   *os.File
	size   int64
	db     *db.Client
	mu     sync.Mutex
	level  Level
	logDir string
	rot    Rotation
}

// New creates a logger that writes to a log file in logDir.
// If db is provided, logs also go to SQLite.
func New(logDir string, dbClient *db.Client, level Level, rot Rotation) (*Logger, error) {
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

	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}

	l := &Logger{
		file:   f,
		size:   size,
		db:     dbClient,
		level:  level,
		logDir: logDir,
		rot:    rot,
	}
	l.pruneOldLogs()
	return l, nil
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
	belowThreshold := severity(level) < severity(l.level)

	// Per-task logs are always persisted to the DB so the dashboard's task
	// detail view shows the full agent stream (which the runner emits at DEBUG)
	// regardless of the service-wide log level. The console mirror in the
	// dispatcher prints every entry too, so without this the dashboard would
	// show nothing while the console showed the whole stream. Service logs and
	// the shared apiary.log file still respect the threshold.
	if belowThreshold && taskID == "" {
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

	// Write to the shared log file only when at/above threshold, to avoid
	// flooding apiary.log with the verbose per-task stream. The stream is still
	// captured per task in the DB (and in the per-task log file).
	if !belowThreshold {
		line := fmt.Sprintf("[%s] [%s] %s\n", entry.Timestamp.Format("2006-01-02 15:04:05"), level, msg)
		if component != "" {
			line = fmt.Sprintf("[%s] [%s] [%s] %s\n", entry.Timestamp.Format("2006-01-02 15:04:05"), level, component, msg)
		}
		l.writeLine(line)
	}

	// Write to database
	if l.db != nil {
		if taskID != "" {
			l.db.WriteTaskLog(ctx, taskID, string(level), msg)
		} else {
			l.db.WriteServiceLog(ctx, string(level), msg, component)
		}
	}
}

// writeLine appends a line to apiary.log, rotating first when the write would
// push the file past the configured size limit. Caller must hold l.mu.
func (l *Logger) writeLine(line string) {
	if l.rot.MaxSizeMB > 0 && l.size > 0 && l.size+int64(len(line)) > int64(l.rot.MaxSizeMB)*1024*1024 {
		l.rotate()
	}
	n, _ := l.file.Write([]byte(line))
	l.size += int64(n)
}

// rotate closes apiary.log, shifts existing backups up one slot
// (apiary.log.1 -> apiary.log.2, ..., dropping the oldest), and reopens a
// fresh file. With MaxBackups <= 0 the current file is simply removed.
// Caller must hold l.mu.
func (l *Logger) rotate() {
	logFile := filepath.Join(l.logDir, "apiary.log")

	l.file.Close()

	if l.rot.MaxBackups > 0 {
		for i := l.rot.MaxBackups - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", logFile, i)
			dst := fmt.Sprintf("%s.%d", logFile, i+1)
			os.Remove(dst) // os.Rename does not overwrite on Windows
			os.Rename(src, dst)
		}
		os.Remove(logFile + ".1")
		os.Rename(logFile, logFile+".1")
	} else {
		os.Remove(logFile)
	}

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Leave size past the limit so the next write retries the rotation
		// (and the reopen) instead of silently giving up for good.
		return
	}
	l.file = f
	l.size = 0
	l.pruneOldLogs()
}

// pruneOldLogs deletes rotated backups (apiary.log.N) and per-task log files
// older than the retention window. Files touched within the window — including
// logs of still-running tasks — are left alone. Pruning is best-effort
// housekeeping, so errors are ignored.
func (l *Logger) pruneOldLogs() {
	if l.rot.MaxAgeDays <= 0 || l.logDir == "" {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -l.rot.MaxAgeDays)

	backups, _ := filepath.Glob(filepath.Join(l.logDir, "apiary.log.*"))
	taskLogs, _ := filepath.Glob(filepath.Join(l.logDir, "tasks", "*.log"))
	for _, path := range append(backups, taskLogs...) {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().Before(cutoff) {
			os.Remove(path)
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
