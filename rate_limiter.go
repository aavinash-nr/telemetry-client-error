package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter tracks requests to simulate throttling
type RateLimiter struct {
	mutex         sync.Mutex
	requests      map[string][]time.Time
	window        time.Duration
	singleIpBurst int
	multiIpBurst  int
}

// NewRateLimiter creates and initializes the rate limiter
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		requests:      make(map[string][]time.Time),
		window:        10 * time.Second, // Track requests over a 10-second window
		singleIpBurst: 20,               // Trigger "queueing" if >20 requests from one IP
		multiIpBurst:  10,               // Trigger "uncooperative hang" if >10 unique IPs
	}
}

// ServeHTTP is the handler that contains the rate-limiting logic
func (rl *RateLimiter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}

	rl.mutex.Lock()

	// Clean up old requests
	now := time.Now()
	uniqueIpsInWindow := 0
	for rip, timestamps := range rl.requests {
		var recent []time.Time
		for _, ts := range timestamps {
			if now.Sub(ts) < rl.window {
				recent = append(recent, ts)
			}
		}
		if len(recent) > 0 {
			rl.requests[rip] = recent
			uniqueIpsInWindow++
		} else {
			delete(rl.requests, rip)
		}
	}

	rl.requests[ip] = append(rl.requests[ip], now)
	requestsFromThisIp := len(rl.requests[ip])
	rl.mutex.Unlock() // Unlock after updating state

	// --- THE CORE LOGIC ---
	// Check for the "Lambda" pattern: many unique IPs
	if uniqueIpsInWindow > rl.multiIpBurst {
		log.Printf("[THROTTLING] Detected distributed load from %d unique IPs. Simulating UNCOOPERATIVE HANG for %s.", uniqueIpsInWindow, ip)
		hangAfterReading(w, r)
		return
	}

	// Check for the "Local Test" pattern: many requests from one IP
	if requestsFromThisIp > rl.singleIpBurst {
		delay := time.Duration(requestsFromThisIp-rl.singleIpBurst) * 6 * time.Second
		log.Printf("[QUEUEING] Detected single-source burst from %s (%d requests). Cooperatively delaying response by %s.", ip, requestsFromThisIp, delay)
		time.Sleep(delay)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("202 Accepted (after queueing delay)"))
		return
	}

	// If no limits are hit, respond quickly
	log.Printf("[OK] Normal request from %s. Responding immediately.", ip)
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("202 Accepted"))
}

// hangAfterReading is your new, better method for simulating the uncooperative hang
func hangAfterReading(w http.ResponseWriter, r *http.Request) {
	// Read and discard the body to ensure the client is now waiting for headers.
	io.Copy(io.Discard, r.Body)
	
	log.Printf("Request from %s read. Now hanging to trigger 'awaiting headers' timeout on the client...", r.RemoteAddr)
	
	// Wait until the client gives up and closes the connection.
	<-r.Context().Done()
	log.Printf("Client from %s closed the connection.", r.RemoteAddr)
}

func main() {
	rateLimiter := NewRateLimiter()
	server := &http.Server{
		Addr:    ":8083", // Use a new port
		Handler: rateLimiter,
	}

	log.Println("Starting final intelligent rate-limiting server on http://localhost:8083...")
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Could not start server: %s\n", err)
	}
}