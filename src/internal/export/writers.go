package export

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Writer renders rows for one output format. Close flushes and finishes the
// document; a JSON file is not valid until Close has run.
type Writer interface {
	Write(Row) error
	Close() error
}

// NewWriter picks a writer by format name: "csv" or "json".
func NewWriter(format string, w io.Writer, cols []Column) (Writer, error) {
	switch strings.ToLower(format) {
	case "csv":
		return NewCSVWriter(w, cols)
	case "json":
		return NewJSONWriter(w, cols), nil
	default:
		return nil, fmt.Errorf("unknown format %q: want csv or json", format)
	}
}

// csvWriter emits a header row then one line per row. Timestamps are RFC3339
// UTC, cost_usd has six decimals, booleans are true/false, NULL is an empty
// cell, and error_message is collapsed to a single line so a spreadsheet's
// row count matches the record count.
type csvWriter struct {
	cw   *csv.Writer
	cols []Column
}

// NewCSVWriter writes the header immediately so an empty export still carries
// the column contract.
func NewCSVWriter(w io.Writer, cols []Column) (Writer, error) {
	cw := csv.NewWriter(w)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Name
	}
	if err := cw.Write(header); err != nil {
		return nil, err
	}
	return &csvWriter{cw: cw, cols: cols}, nil
}

func (c *csvWriter) Write(r Row) error {
	rec := make([]string, len(c.cols))
	for i, col := range c.cols {
		rec[i] = csvCell(col, r[i])
	}
	return c.cw.Write(rec)
}

func (c *csvWriter) Close() error {
	c.cw.Flush()
	return c.cw.Error()
}

func csvCell(col Column, v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if col.Name == "error_message" {
			return singleLine(t)
		}
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if col.Name == "cost_usd" {
			return strconv.FormatFloat(t, 'f', 6, 64)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case time.Time:
		return formatTime(t)
	default:
		return fmt.Sprint(v)
	}
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\r", " ")), " ")
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// jsonWriter streams a JSON array of objects: "[" up front, one object per
// row, "]" on Close. It never holds more than one row, so an export with
// transcripts included runs in constant memory.
type jsonWriter struct {
	bw    *bufio.Writer
	cols  []Column
	count int
}

// NewJSONWriter returns a writer whose output is a valid JSON array once
// Close has been called. Every key is present in every object; NULL columns
// are explicit null rather than omitted, so a column list derived from any one
// object is complete.
func NewJSONWriter(w io.Writer, cols []Column) Writer {
	return &jsonWriter{bw: bufio.NewWriter(w), cols: cols}
}

func (j *jsonWriter) Write(r Row) error {
	if j.count == 0 {
		if _, err := j.bw.WriteString("[\n"); err != nil {
			return err
		}
	} else {
		if _, err := j.bw.WriteString(",\n"); err != nil {
			return err
		}
	}
	j.count++

	if err := j.bw.WriteByte('{'); err != nil {
		return err
	}
	for i, col := range j.cols {
		if i > 0 {
			if err := j.bw.WriteByte(','); err != nil {
				return err
			}
		}
		key, _ := json.Marshal(col.Name)
		val, err := json.Marshal(jsonValue(r[i]))
		if err != nil {
			return fmt.Errorf("encode %s: %w", col.Name, err)
		}
		if _, err := j.bw.Write(key); err != nil {
			return err
		}
		if err := j.bw.WriteByte(':'); err != nil {
			return err
		}
		if _, err := j.bw.Write(val); err != nil {
			return err
		}
	}
	return j.bw.WriteByte('}')
}

func (j *jsonWriter) Close() error {
	if j.count == 0 {
		if _, err := j.bw.WriteString("[]\n"); err != nil {
			return err
		}
		return j.bw.Flush()
	}
	if _, err := j.bw.WriteString("\n]\n"); err != nil {
		return err
	}
	return j.bw.Flush()
}

func jsonValue(v any) any {
	if t, ok := v.(time.Time); ok {
		return formatTime(t)
	}
	return v
}
