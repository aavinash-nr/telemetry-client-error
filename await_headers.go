package main

import (
	"io"
	"log"
	"net/http"
	"time"
)

func awaitHeadersHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received request from %s. Reading request body...", r.RemoteAddr)

	// By reading and discarding the body, we ensure the client has finished sending
	// and is now waiting for our response headers.
	_, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		log.Printf("Error reading body: %v", err)
		return
	}
	
	log.Printf("Request body read. Now hanging to trigger 'awaiting headers' timeout on the client...")
	time.Sleep(5 * time.Second) // Simulate a delay before sending the response

	// Now we do nothing. The client is stuck waiting for the HTTP status and headers.
	// This will block until the client gives up and closes the connection.
	<-r.Context().Done()
	log.Printf("Client from %s closed the connection.", r.RemoteAddr)
}

func main() {
	http.HandleFunc("/", awaitHeadersHandler)

	// Start the server on a new port to avoid conflicts
	port := "8082"
	log.Printf("Starting 'Awaiting Headers' server on http://localhost:%s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Could not start server: %s\n", err)
	}
}