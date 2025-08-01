# Simulating a Telemetry Client Timeout Problem

## 1\. The Problem We Faced

Our application was getting stuck when sending a high volume of logs. We noticed that HTTP requests were taking more than 10 seconds to fail, even though our timeout was set to only 2.4 seconds.

This issue was only happening under heavy load, specifically when many requests were sent at the same time (concurrently), like from our AWS Lambda functions.

## 2\. What We Discovered

After a detailed investigation, we confirmed that **the problem was not a bug in our code**. It was caused by the **API Rate Limiter** on the external server we were sending data to.

The server showed two different defensive behaviors based on the traffic pattern:

  * **Queuing Delay:** When we sent a large number of requests from a single computer, the server put them in a queue. This created a manageable lag of 4-6 seconds, like waiting in line at a busy shop.
  * **Uncooperative Hang:** When many requests came from different Lambda functions all at once, the server's security system saw this as a potential threat. It would accept the connection and then intentionally not respond for over 10 seconds. This "uncooperative" behavior is a defense mechanism, and it was causing our client's timeout logic to fail.

## 3\. About These Test Files

We created these files to safely reproduce and demonstrate these behaviors.

  * **`main.go` (The Client Tester)**
    This is our test client. It sends many concurrent requests to a server to check for timeouts.

      * **Configuration:** You can set the `endpoint` and `concurrentRequests` at the top of the file.
      * **Output:** It will create log files in the `error_logs/` directory for any request that takes longer than 5 seconds.

  * **`await_headers_server.go` (The Uncooperative Server)**
    This is a simple server that simulates the "uncooperative hang." It accepts a request and then hangs forever without responding. We use this to reliably prove how our client's timeout fails.

  * **`rate_limiter_server.go` (The Smart Server)**
    This is a more intelligent server that simulates the real New Relic server's behavior.

      * It creates a **queuing delay** for traffic from one computer.
      * It creates an **uncooperative hang** for traffic from many different computers at once.

## 4\. How to Run the Tests

Here is how you can use these files to see the problem for yourself.

### Test 1: Replicating the Timeout Failure

This test proves how the client timeout fails against an uncooperative server.

1.  **Start the Server:**
    In your first terminal, start the uncooperative server.
    ```sh
    go run await_headers_server.go
    ```
2.  **Configure the Client:**
    In `main.go`, make sure the `endpoint` is set to the correct server.
    ```go
    const endpoint = "http://localhost:8082"
    ```
3.  **Run the Client:**
    In a second terminal, run the client application.
    ```sh
    go run main.go
    ```
4.  **Check the Result:**
    The client will take a long time to finish. The console will print `DETECTED SLOW REQUEST`, and you will find detailed log files in the `error_logs/` folder.

### Test 2: Simulating the Queuing Delay

This test shows the gentler rate-limiting behavior.

1.  **Start the Server:**
    In your first terminal, start the smart rate-limiting server.
    ```sh
    go run rate_limiter_server.go
    ```
2.  **Configure the Client:**
    In `main.go`, change the `endpoint` to the smart server.
    ```go
    const endpoint = "http://localhost:8083"
    ```
3.  **Run the Client:**
    In a second terminal, run the client application.
    ```sh
    go run main.go
    ```
4.  **Check the Result:**
    Look at the server's logs. You will see `[QUEUEING]` messages. The client may also detect some slow requests depending on the load.
