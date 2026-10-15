package metrics

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// sample is one line of OpenMetrics text: a metric name, its labels, a
// value, and an optional timestamp.
type sample struct {
	Name   string
	Labels map[string]string
	Value  float64
	Time   time.Time // zero when the line has no timestamp
}

// parseText reads the OpenMetrics (or Prometheus) text format: comment
// lines starting with #, and sample lines of the form
//
//	name{label="value",...} value [timestamp]
//
// Temporal Cloud writes timestamps in milliseconds; OpenMetrics allows
// seconds, so a value below 1e11 is read as seconds.
func parseText(r io.Reader) ([]sample, error) {
	var out []sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		s, err := parseLine(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

func parseLine(text string) (sample, error) {
	s := sample{Labels: map[string]string{}}
	i := strings.IndexAny(text, "{ ")
	if i < 0 {
		return s, fmt.Errorf("no value in %q", text)
	}
	s.Name = text[:i]
	rest := text[i:]
	if strings.HasPrefix(rest, "{") {
		end, err := parseLabels(rest, s.Labels)
		if err != nil {
			return s, err
		}
		rest = rest[end:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return s, fmt.Errorf("no value in %q", text)
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return s, fmt.Errorf("value %q: %w", fields[0], err)
	}
	s.Value = v
	if len(fields) > 1 {
		ts, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return s, fmt.Errorf("timestamp %q: %w", fields[1], err)
		}
		if ts < 1e11 {
			sec, frac := math.Modf(ts)
			s.Time = time.Unix(int64(sec), int64(frac*1e9)).UTC()
		} else {
			s.Time = time.UnixMilli(int64(ts)).UTC()
		}
	}
	return s, nil
}

// parseLabels reads {a="x",b="y"} from the start of s into labels and
// returns the index just past the closing brace.
func parseLabels(s string, labels map[string]string) (int, error) {
	i := 1 // past {
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			return 0, fmt.Errorf("unterminated labels in %q", s)
		}
		if s[i] == '}' {
			return i + 1, nil
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return 0, fmt.Errorf("label without = in %q", s)
		}
		name := strings.TrimSpace(s[i : i+eq])
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return 0, fmt.Errorf("label %s isn't quoted", name)
		}
		i++
		var b strings.Builder
		for ; i < len(s) && s[i] != '"'; i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			b.WriteByte(s[i])
		}
		if i >= len(s) {
			return 0, fmt.Errorf("unterminated value for label %s", name)
		}
		labels[name] = b.String()
		i++ // past closing quote
	}
}
