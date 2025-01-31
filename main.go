package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var srcDir = "./Input"       // Source directory
var destDir = "./Output"     // Destination directory
var srcDirCB = "./InputCB"   // Source directory
var destDirCB = "./OutputCB" // Destination directory

var failureStats = make(map[string]int)
var failureArcives = make(map[string]int)

type GetStats struct {
	Reason string `json:"reason"`
}

type FailedBatch struct {
	Reason   string `json:"reason"`
	FileName string `json:"fileName"`
	Origin   string `json:"origin"`
}

type FailedBatchReason struct {
	Reason string `json:"reason"`
}

// Handle a generic request and add CORS headers
func handleCORS(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")                                                                            // Allow all origins (replace with specific origins in production)
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")                                             // Allowed HTTP methods
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Access-Control-Allow-Headers, Authorization, X-Requested-With") // Allowed headers

	// Handle preflight (OPTIONS) request
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleCORS(w, r) // Set CORS headers for the root route
	})
	http.HandleFunc("/live-updates", liveUpdatesHandler)
	http.HandleFunc("/reasons", getstats)
	http.HandleFunc("/reprocess", reprocess)
	http.ListenAndServe("localhost:8080", nil)
}

// Start of playground
var (
	updateChannel = make(chan string) // Shared channel for broadcasting updates
	mu            sync.Mutex          // Mutex for thread-safe access to the channel
)

// this will be used by other endpoints to notify the user of the events like what endpoint is being request with the type of repuest
// notify will be called by other endpoints to send updates
func notify(message string) {
	mu.Lock()
	defer mu.Unlock()
	// Send the message to the update channel
	go func() {
		updateChannel <- message
	}()
}

// liveUpdatesHandler streams updates to clients connected to this endpoint
func liveUpdatesHandler(w http.ResponseWriter, r *http.Request) {
	handleCORS(w, r)

	// Set necessary headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Listen to updates and stream them to the client
	for {
		select {
		case update := <-updateChannel:
			fmt.Fprintf(w, "data: %s\n\n", update) // Send the update
			w.(http.Flusher).Flush()               // Push the update to the client immediately
		case <-r.Context().Done(): // If the client disconnects
			fmt.Println("Client disconnected")
			return
		}
	}
}

// End of playground

// This is to copy the file to the requests folder for reprocessing
func copyFile(fileName string, from string, to string) bool {
	if !strings.HasSuffix(fileName, ".xml") {
		fileName += ".xml"
	}
	matches, err := filepath.Glob(filepath.Join(from, fileName))

	//Throw error if file is not found
	if err != nil {
		time.Sleep(20 * time.Second)
		fmt.Println("The file", fileName, "was not found, please try again...")
		return false
	}
	if len(matches) == 0 {
		fmt.Println("The file", fileName, "was not found, please try again...")
		return false
	}

	inputFile, err := os.Open(matches[0])
	defer inputFile.Close()
	//Creat the file we want to clone
	outputFile, err := os.Create(filepath.Join(to, fileName+".xml"))
	if err != nil {
		log.Fatal("file cloning error")
	}
	defer outputFile.Close()
	// Calling Sleep method to give xml time to be copied befor the .trg is created 20 milisecond wait
	time.Sleep(20 * time.Millisecond)
	//Creat the .TRG file of the clone
	outputTRGFile, err := os.Create(filepath.Join(to, fileName+".trg"))
	if err != nil {
		log.Fatal("file cloning error")
	}
	defer outputTRGFile.Close()
	//copy the data to the clone file
	_, err = io.Copy(outputFile, inputFile)
	if err != nil {
		log.Fatal("faild to copy to output file")
	}
	return true
}

// This is a conductor for handling different HTTP methods (GET, POST) for the /getstats endpoint.
// It routes the request to the appropriate handler based on the HTTP method:
// - If the method is GET, it returns a status of 'active' and calls getStatsHelp to provide assistance or documentation.
// - If the method is POST, it returns a status of 'found' and processes the details using getstatsDetails.
// - If the method is not GET or POST, it returns a "Method Not Allowed" error and calls getStatsHelp for further guidance.
func getstats(w http.ResponseWriter, r *http.Request) {
	handleCORS(w, r)
	switch r.Method {
	case http.MethodGet:
		{
			w.WriteHeader(http.StatusOK)
			getStatsHelp(w, r)

		}
	case http.MethodPost:
		{
			var reason FailedBatchReason
			json.NewDecoder(r.Body).Decode(&reason)
			defer r.Body.Close()
			switch reason.Reason {
			case "all":
				showAllData(w) // Call a function to handle "all" case
			case "getTotal":
				getTotalFalures(w)
			case "lookInto":
				getFaluresToLookInto(w)
			default:
				{
					w.WriteHeader(http.StatusFound)
					getstatsDetails(w, r, reason.Reason)
				}
			}
		}
	default:
		{
			http.Error(w, "Method not allowed", http.StatusForbidden)
			getStatsHelp(w, r)
		}
	}
}

func reprocess(w http.ResponseWriter, r *http.Request) {
	var file FailedBatch

	json.NewDecoder(r.Body).Decode(&file)
	defer r.Body.Close()

	go func(file FailedBatch) {
		time.Sleep(30 * time.Second) // wait before processing so if the service is down things wont just fail again | give the service some time to get back up
		processFile(file)
	}(file)

	// Notify about this endpoint being called
	//notify(fmt.Sprintf("Endpoint: %s | Method: %s", r.URL.Path, r.Method))

	notify(fmt.Sprintf("Endpoint: %s is going to be reprocessed...", file.FileName))

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode("File reprocessing has been scheduled")

}

func getFaluresToLookInto(w http.ResponseWriter) {

	var failuresToLookInto []map[string]interface{}

	// Loop through the failureArcives map and filter failures with count >= 2
	for fileName, occurrences := range failureArcives {
		if occurrences >= 2 {
			// Append directly as a map
			failuresToLookInto = append(failuresToLookInto, map[string]interface{}{
				"fileName": fileName,
				"count":    occurrences,
			})
		}
	}

	// Respond with a list of failures that need to be looked into
	w.Header().Set("Content-Type", "application/json")
	if len(failuresToLookInto) > 0 {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(failuresToLookInto)
	} else {
		w.WriteHeader(http.StatusNoContent)
		json.NewEncoder(w).Encode("No failures to look into.")
	}
}

func getTotalFalures(w http.ResponseWriter) {
	// Calculate the total
	totalFailures := 0
	for _, count := range failureStats {
		totalFailures += count
	}

	json.NewEncoder(w).Encode(totalFailures)
}

func showAllData(w http.ResponseWriter) {

	// Initialize arrays for labels and data
	var labels []string
	var data []int

	// Populate the arrays from the map
	for label, value := range failureStats {
		labels = append(labels, label)
		data = append(data, value)
	}

	// Create a response structure
	chartData := struct {
		Labels []string `json:"labels"`
		Data   []int    `json:"data"`
	}{
		Labels: labels,
		Data:   data,
	}

	// Set header and encode the response as JSON
	w.Header().Set("Content-Type", "application/json")
	// w.WriteHeader(http.StatusOK) // Use 200 for successful response
	json.NewEncoder(w).Encode(chartData)
}

// show how many failures each reason has
func getstatsDetails(w http.ResponseWriter, r *http.Request, reason string) {
	value := failureStats[reason]
	w.WriteHeader(http.StatusFound)
	json.NewEncoder(w).Encode(reason + " had a total of: " + strconv.Itoa(value) + " falure(s).")
}

func getStatsHelp(w http.ResponseWriter, r *http.Request) {
	// Collect all reasons into a slice(array)
	var reasons []string

	for reason := range failureStats {
		reasons = append(reasons, reason)
	}
	// Join the reasons into a single string
	reasonsList := strings.Join(reasons, ", ")
	// Print the message
	message := fmt.Sprintf(
		`Available options include: [%s] 
		Example usage is: {"reason"="No response from enrchimnet"}`, reasonsList)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(message)
}

func lookInTo(fileName string) {

	notify(fmt.Sprintf("Endpoint: %s failed again and need to be looked into", fileName))

	fmt.Println("File:", fileName, "has failed more than once, please look into it")
}

// this is just here to give it some time befor it reprocesses the failed itmes
func wait() {
	time.Sleep(30 * time.Second)
}

func processFile(file FailedBatch) {
	// Look for the key in the map, if not found we add it to the list so if it pop's up again we can look into it
	if value, exists := failureArcives[file.FileName]; exists {
		fmt.Printf("Key '%s' exists with value %d\n", file.FileName, value)
		failureArcives[file.FileName]++
		lookInTo(file.FileName)

		return
	} else {

		fmt.Printf("Key '%s' does not exist in the map and will be reprocessed \n", file.FileName)
		failureArcives[file.FileName]++
	}

	switch file.Origin {
	case "BANCS":
		{
			if copyFile(file.FileName, srcDir, destDir) {
				failureStats[file.Reason]++
				//w.WriteHeader(http.StatusOK)
				//json.NewEncoder(w).Encode("File:" + file.FileName + " has been placed in the destiation for reporcessing")
			} else {
				failureStats[file.Reason]++
				//w.WriteHeader(http.StatusBadRequest)
				//json.NewEncoder(w).Encode("The file name entered(" + file.FileName + ") is incorrect, please try again...")
			}
		}
	case "BANCSCB":
		{
			if copyFile(file.FileName, srcDirCB, destDirCB) {
				failureStats[file.Reason]++
				//w.WriteHeader(http.StatusOK)
				//json.NewEncoder(w).Encode("File:" + file.FileName + " has been placed in the destiation for reporcessing")
			} else {
				failureStats[file.Reason]++
				//w.WriteHeader(http.StatusBadRequest)
				//json.NewEncoder(w).Encode("The file name entered(" + file.FileName + ") is incorrect, please try again...")
			}
		}

	default:
		{
			//http.Error(w, "Invalid Origin", http.StatusBadRequest)
			return
		}
	}
}
