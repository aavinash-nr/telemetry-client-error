package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	math_rand "math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// STEP 1: CONFIGURE YOUR TEST
// =================================================================
const (
	licenseKey         = "api-key"                 // <--- PASTE YOUR KEY HERE
	endpoint           = "http://localhost:8083"   // Use 8080 for the SLOW server
	concurrentRequests = 4000                      // <-- SET HOW MANY CONCURRENT REQUESTS
)
// =================================================================

// --- Test Configuration ---
const (
	SendTimeoutRetryBase  = 200 * time.Millisecond
	SendTimeoutMaxRetries = 5
	SendTimeoutMaxBackOff = 3 * time.Second
	httpClientTimeout     = 2400 * time.Millisecond
)

// --- Structs ---
type Client struct {
	httpClient *http.Client
	timeout    time.Duration
	licenseKey string
	endpoint   string
	logger     *log.Logger
	requestID  string
}
type requestBuilder func(buffer *bytes.Buffer) (*http.Request, error)
type AttemptData struct {
	Error        error
	ResponseBody string
	Response     *http.Response
}

// --- Main Test Function ---
func main() {
	if licenseKey == "YOUR_NEW_RELIC_LICENSE_KEY" || licenseKey == "" {
		log.Fatal("ERROR: Please paste your New Relic license key.")
	}

	// =================================================================
	// NEW: Set up a single master log file for ALL requests
	// =================================================================
	logFile, err := os.Create("master_test_log.txt")
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer logFile.Close()

	// Create a single logger that writes to the file
	logger := log.New(logFile, "", log.LstdFlags|log.Lmicroseconds)
	// Use a mutex to prevent concurrent writes from garbling the log file
	var logMutex sync.Mutex

	log.Printf("Starting test with %d requests. All logs will be saved to 'master_test_log.txt'\n", concurrentRequests)

	var wg sync.WaitGroup
	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func(requestNum int) {
			defer wg.Done()
			requestID := fmt.Sprintf("test-%d", requestNum)
			
			// Each goroutine will use the shared logger and mutex
			client := NewClient(licenseKey, endpoint, logger, requestID, &logMutex)

			dummyPayload := []*bytes.Buffer{bytes.NewBufferString(`{"test": "payload"}`)}
			var builder requestBuilder = func(buffer *bytes.Buffer) (*http.Request, error) {
				req, err := http.NewRequestWithContext(context.Background(), "POST", client.endpoint, buffer)
				if err != nil { return nil, err }
				req.Header.Add("Api-Key", client.licenseKey)
				req.Header.Add("Content-Type", "application/json")
				return req, nil
			}

			successCount, sentBytes := client.sendPayloads(dummyPayload, builder)
			client.Logf("Request completed - Success: %d, Bytes sent: %d", successCount, sentBytes)
		}(i + 1)
	}

	wg.Wait()
	log.Println("Test finished.")
}

// --- Core Logic ---

// Client struct now includes the mutex
type loggerClient struct {
	*Client
	logMutex *sync.Mutex
	requestLogs []string  // Buffer to collect logs for this request
}

func (lc *loggerClient) Logf(format string, args ...interface{}) {
	lc.logMutex.Lock()
	defer lc.logMutex.Unlock()
	prefix := fmt.Sprintf("[%s] ", lc.requestID)
	logEntry := fmt.Sprintf(prefix+format, args...)
	
	// Add to request logs buffer
	timestamp := time.Now().Format("2006/01/02 15:04:05.000000")
	lc.requestLogs = append(lc.requestLogs, fmt.Sprintf("%s %s", timestamp, logEntry))
	
	// Also log to master log
	lc.logger.Printf(prefix+format, args...)
}

func NewClient(key, endpoint string, logger *log.Logger, requestID string, mutex *sync.Mutex) *loggerClient {
	httpClient := &http.Client{
		Timeout: httpClientTimeout,
	}

	var b [8]byte
	_, _ = rand.Read(b[:])
	randomSource := math_rand.NewSource(int64(binary.LittleEndian.Uint64(b[:])))
	_ = math_rand.New(randomSource)

	client := &Client{
		httpClient: httpClient,
		licenseKey: key,
		endpoint:   endpoint,
		timeout:    20 * time.Second,
		logger:     logger,
		requestID:  requestID,
	}
	
	return &loggerClient{Client: client, logMutex: mutex, requestLogs: make([]string, 0)}
}

func (c *loggerClient) sendPayloads(compressedPayloads []*bytes.Buffer, builder requestBuilder) (successCount int, sentBytes int) {
	successCount = 0
	sentBytes = 0
	sendPayloadsStartTime := time.Now()
	var hasSlowRequest bool
	var maxDuration time.Duration
	
	for _, p := range compressedPayloads {
		payloadSize := p.Len()
		sentBytes += payloadSize
		currentPayloadBytes := p.Bytes()

		var response AttemptData

		// buffer this channel to allow successful attempts to go through if possible
		data := make(chan AttemptData, 1)
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()

		go c.attemptSend(ctx, currentPayloadBytes, builder, data, &hasSlowRequest, &maxDuration)

		select {
		case <-ctx.Done():
			response.Error = fmt.Errorf("failed to send data within user defined timeout period: %s", c.timeout.String())
			c.Logf("Telemetry client error: %s, payload size: %d bytes", response.Error, payloadSize)
		case response = <-data:
		}

		if response.Error != nil {
			c.Logf("Telemetry client error: %s, payload size: %d bytes", response.Error, payloadSize)
			sentBytes -= payloadSize
		} else if response.Response.StatusCode >= 300 {
			c.Logf("Telemetry client response: [%s] %s", response.Response.Status, response.ResponseBody)
		} else {
			successCount += 1
		}
	}

	sendPayloadsDuration := time.Since(sendPayloadsStartTime)
	c.Logf("sendPayloads: took %s to finish sending all payloads", sendPayloadsDuration.String())
	
	// Save all logs if any request in this payload was slow
	if hasSlowRequest {
		c.saveAllRequestLogs(maxDuration)
	}
	
	return successCount, sentBytes
}

func (c *loggerClient) attemptSend(ctx context.Context, currentPayloadBytes []byte, builder requestBuilder, dataChan chan AttemptData, hasSlowRequest *bool, maxDuration *time.Duration) {
	baseSleepTime := SendTimeoutRetryBase
	var localMaxDuration time.Duration
	var localHasSlowRequest bool

	for attempts := 0; attempts < SendTimeoutMaxRetries; attempts++ {
		select {
		case <-ctx.Done():
			dataChan <- AttemptData{Error: ctx.Err()}
			return
		default:
		}

		req, err := builder(bytes.NewBuffer(currentPayloadBytes))
		if err != nil {
			dataChan <- AttemptData{Error: err}
			return
		}

		httpStart := time.Now()
		res, err := c.httpClient.Do(req)
		httpDuration := time.Since(httpStart)

		// Track the maximum duration and check if any request is slow
		if httpDuration > localMaxDuration {
			localMaxDuration = httpDuration
		}
		if httpDuration > httpClientTimeout {
			localHasSlowRequest = true
		}

		if err == nil {
			c.Logf("[ATTEMPT_%d] SUCCESS. Duration: %s, Status: %s", 
				attempts+1, httpDuration, res.Status)
			
			// Read response body if needed for logging
			responseBody := ""
			if res.StatusCode >= 300 {
				bodyBytes := make([]byte, 1024) // Read up to 1KB for error responses
				n, _ := res.Body.Read(bodyBytes)
				responseBody = string(bodyBytes[:n])
			}
			res.Body.Close()
			
			// Update shared variables with mutex protection
			c.logMutex.Lock()
			if localMaxDuration > *maxDuration {
				*maxDuration = localMaxDuration
			}
			if localHasSlowRequest {
				*hasSlowRequest = true
			}
			c.logMutex.Unlock()
			
			dataChan <- AttemptData{Error: nil, Response: res, ResponseBody: responseBody}
			return
		}

		c.Logf("[ATTEMPT_%d] HTTP Error. Type: %T, Details: %v, Duration: %s",
			attempts+1, err, err, httpDuration)

		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			retryDelay := baseSleepTime
			c.Logf("[ATTEMPT_%d] Timeout error, retrying after %s", attempts+1, retryDelay)
			time.Sleep(retryDelay)
		} else {
			// Update shared variables with mutex protection
			c.logMutex.Lock()
			if localMaxDuration > *maxDuration {
				*maxDuration = localMaxDuration
			}
			if localHasSlowRequest {
				*hasSlowRequest = true
			}
			c.logMutex.Unlock()
			
			dataChan <- AttemptData{Error: err}
			return
		}
	}

	c.Logf("All retries exhausted.")
	
	// Update shared variables with mutex protection
	c.logMutex.Lock()
	if localMaxDuration > *maxDuration {
		*maxDuration = localMaxDuration
	}
	if localHasSlowRequest {
		*hasSlowRequest = true
	}
	c.logMutex.Unlock()
	
	dataChan <- AttemptData{Error: fmt.Errorf("all retries failed")}
}

// Helper function to create directory structure for duration-based logs
func createDurationDirectory(durationSeconds int) error {
	dirPath := filepath.Join("error_logs", fmt.Sprintf("%d", durationSeconds))
	return os.MkdirAll(dirPath, 0755)
}

// Helper function to save all collected logs for a slow request
func (c *loggerClient) saveAllRequestLogs(maxDuration time.Duration) {
	durationSeconds := int(maxDuration.Seconds())
	
	// Only save if duration is more than 3 seconds
	if durationSeconds <= 3 {
		return
	}
	
	// Create directory for this duration
	if dirErr := createDurationDirectory(durationSeconds); dirErr != nil {
		c.Logf("Failed to create directory for duration %d: %v", durationSeconds, dirErr)
		return
	}
	
	// Create filename with test ID and duration
	filename := fmt.Sprintf("testid_%s_%ds.log", c.requestID, durationSeconds)
	filePath := filepath.Join("error_logs", fmt.Sprintf("%d", durationSeconds), filename)
	
	// Open or create the log file
	file, fileErr := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if fileErr != nil {
		c.Logf("Failed to create slow request log file %s: %v", filePath, fileErr)
		return
	}
	defer file.Close()
	
	// Write header for this request session
	fmt.Fprintf(file, "\n=== REQUEST %s - Max Duration: %s ===\n", c.requestID, maxDuration)
	
	// Write all collected logs for this request
	for _, logEntry := range c.requestLogs {
		fmt.Fprintf(file, "%s\n", logEntry)
	}
	
	fmt.Fprintf(file, "=== END REQUEST %s ===\n\n", c.requestID)
}