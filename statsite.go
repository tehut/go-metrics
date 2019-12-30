package metrics

import (
	"bufio"
	"fmt"

	"net"
	"net/url"
	"strings"
	"time"

	logger "github.com/armon/go-metrics/logger"
)

const (
	// We force flush the statsite metrics after this period of
	// inactivity. Prevents stats from getting stuck in a buffer
	// forever.
	flushInterval = 100 * time.Millisecond
)

// NewStatsiteSinkFromURL creates an StatsiteSink from a URL. It is used
// (and tested) from NewMetricSinkFromURL uses go-hclog by default.
func NewStatsiteSinkFromURL(u *url.URL) (MetricSink, error) {
	return NewStatsiteSink(u.Host)
}

// NewStatsiteSinkFromURLWithCustomLogger creates an StatsiteSink from a URL.
// It allows use of a custom logger
func NewStatsiteSinkFromURLWithCustomLogger(u *url.URL, l logger.Logger) (MetricSink, error) {
	return NewStatsiteSinkWithCustomLogger(u.Host, l)
}

// StatsiteSink provides a MetricSink that can be used with a
// statsite metrics server
type StatsiteSink struct {
	addr        string
	metricQueue chan string
	logger      logger.Logger
}

// NewStatsiteSink is used to create a new StatsiteSink
func NewStatsiteSink(addr string) (*StatsiteSink, error) {
	l := &logger.OurLogger{}
	s := &StatsiteSink{
		addr:        addr,
		metricQueue: make(chan string, 4096),
		logger:      l.New(),
	}
	go s.flushMetrics()
	return s, nil
}

// NewStatsiteSinkWithCustomLogger is used to create a new StatsiteSink with a custom logger
func NewStatsiteSinkWithCustomLogger(addr string, l logger.Logger) (*StatsiteSink, error) {
	s := &StatsiteSink{
		addr:        addr,
		metricQueue: make(chan string, 4096),
		logger:      l,
	}
	go s.flushMetrics()
	return s, nil
}

// Close is used to stop flushing to statsite
func (s *StatsiteSink) Shutdown() {
	close(s.metricQueue)
}

func (s *StatsiteSink) SetGauge(key []string, val float32) {
	flatKey := s.flattenKey(key)
	s.pushMetric(fmt.Sprintf("%s:%f|g\n", flatKey, val))
}

func (s *StatsiteSink) SetGaugeWithLabels(key []string, val float32, labels []Label) {
	flatKey := s.flattenKeyLabels(key, labels)
	s.pushMetric(fmt.Sprintf("%s:%f|g\n", flatKey, val))
}

func (s *StatsiteSink) EmitKey(key []string, val float32) {
	flatKey := s.flattenKey(key)
	s.pushMetric(fmt.Sprintf("%s:%f|kv\n", flatKey, val))
}

func (s *StatsiteSink) IncrCounter(key []string, val float32) {
	flatKey := s.flattenKey(key)
	s.pushMetric(fmt.Sprintf("%s:%f|c\n", flatKey, val))
}

func (s *StatsiteSink) IncrCounterWithLabels(key []string, val float32, labels []Label) {
	flatKey := s.flattenKeyLabels(key, labels)
	s.pushMetric(fmt.Sprintf("%s:%f|c\n", flatKey, val))
}

func (s *StatsiteSink) AddSample(key []string, val float32) {
	flatKey := s.flattenKey(key)
	s.pushMetric(fmt.Sprintf("%s:%f|ms\n", flatKey, val))
}

func (s *StatsiteSink) AddSampleWithLabels(key []string, val float32, labels []Label) {
	flatKey := s.flattenKeyLabels(key, labels)
	s.pushMetric(fmt.Sprintf("%s:%f|ms\n", flatKey, val))
}

// Flattens the key for formatting, removes spaces
func (s *StatsiteSink) flattenKey(parts []string) string {
	joined := strings.Join(parts, ".")
	return strings.Map(func(r rune) rune {
		switch r {
		case ':':
			fallthrough
		case ' ':
			return '_'
		default:
			return r
		}
	}, joined)
}

// Flattens the key along with labels for formatting, removes spaces
func (s *StatsiteSink) flattenKeyLabels(parts []string, labels []Label) string {
	for _, label := range labels {
		parts = append(parts, label.Value)
	}
	return s.flattenKey(parts)
}

// Does a non-blocking push to the metrics queue
func (s *StatsiteSink) pushMetric(m string) {
	select {
	case s.metricQueue <- m:
	default:
	}
}

// Flushes metrics
func (s *StatsiteSink) flushMetrics() {
	var sock net.Conn
	var err error
	var wait <-chan time.Time
	var buffered *bufio.Writer
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

CONNECT:
	// Attempt to connect
	sock, err = net.Dial("tcp", s.addr)
	if err != nil {
		logger.Error("Error connecting to statsite", s.logger, err)
		goto WAIT
	}

	// Create a buffered writer
	buffered = bufio.NewWriter(sock)

	for {
		select {
		case metric, ok := <-s.metricQueue:
			// Get a metric from the queue
			if !ok {
				goto QUIT
			}

			// Try to send to statsite
			_, err := buffered.Write([]byte(metric))
			if err != nil {
				logger.Error("Error writing to statsite", s.logger, err)
				goto WAIT
			}
		case <-ticker.C:
			if err := buffered.Flush(); err != nil {
				logger.Error("Error flushing to statsite", s.logger, err)
				goto WAIT
			}
		}
	}

WAIT:
	// Wait for a while
	wait = time.After(time.Duration(5) * time.Second)
	for {
		select {
		// Dequeue the messages to avoid backlog
		case _, ok := <-s.metricQueue:
			if !ok {
				goto QUIT
			}
		case <-wait:
			goto CONNECT
		}
	}
QUIT:
	s.metricQueue = nil
}
