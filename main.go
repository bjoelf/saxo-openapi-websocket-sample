package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Saxo API and WebSocket Configuration
const (
	apiBaseURL = "https://gateway.saxobank.com/sim/openapi"
	wsBaseURL  = "wss://streaming.saxobank.com/sim/openapi"
)

var (
	fxSpotIDs          = []string{"21", "22"}                         // Modify as needed
	contractFuturesIDs = []string{"37978561", "37978556", "39614794"} // Modify as needed
)

// Replace this with the temporary token provided by Saxo (https://www.developer.saxo/openapi/token/current/)
var accessToken = "" // Temporary access token for testing purposes

// subscribeToPrices subscribes to price updates for a specific instrument
// func subscribeToPrices(accessToken, contextID, referenceID, instrumentID string, assetType string) error {
func subscribeToPrices(accessToken string, subscription map[string]interface{}) error {

	// Convert subscription data to JSON
	subscriptionData, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Errorf("failed to marshal subscription data: %v", err)
	}

	// Create the HTTP POST request
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/trade/v1/infoprices/subscriptions", apiBaseURL), bytes.NewBuffer(subscriptionData))
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Log the full request for debugging
	//log.Printf("Subscription Request Headers: %v", req.Header)
	log.Printf("Subscription Request URL: %s", req.URL.String())
	log.Printf("Subscription Request Body: %s", subscriptionData)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyString := string(bodyBytes)
		return fmt.Errorf("failed to subscribe to prices: %s, response body: %s", resp.Status, bodyString)
	}

	log.Printf("Successfully subscribed:  %s", subscriptionData)
	return nil
}

// connectWebSocket uses the access token to connect to the WebSocket API
func connectWebSocket(accessToken string, contextid string) (*websocket.Conn, error) {

	// Prepare WebSocket connection
	u := url.URL{Scheme: "wss", Host: "streaming.saxobank.com", Path: "/sim/openapi/streamingws/connect"}
	params := url.Values{}
	params.Set("ContextId", contextid)
	u.RawQuery = params.Encode()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)

	// Log the full URL and headers for debugging
	log.Printf("Connecting to WebSocket URL: %s", u.String())
	for k, v := range headers {
		log.Printf("Header: %s: %s", k, v)
	}

	// Custom WebSocket dialer to capture HTTP response
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(u.String(), headers)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			log.Printf("Handshake failed with status: %d, response: %s", resp.StatusCode, string(body))
		}
		return nil, fmt.Errorf("WebSocket dial error: %v", err)
	}

	log.Printf("Connected to WebSocket with Context ID: %s", contextid)
	return conn, nil
}

// handleWebSocket manages the WebSocket connection and message handling
func handleWebSocket(conn *websocket.Conn) {
	defer conn.Close()
	timeout := time.After(1 * time.Minute) // Set the timeout for 1 minute

	for {
		select {
		case <-timeout:
			log.Println("Timeout reached, closing connection")
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("Write close error:", err)
			}
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("Read error:", err)
				return
			}
			handleMessage(message)
		}
	}
}

// handleMessage processes incoming WebSocket messages assuming they are in JSON format
func handleMessage(message []byte) {
	if len(message) < 16 {
		log.Printf("Message too short to be valid: %d bytes", len(message))
		return
	}

	// Byte index 0-8: Message Identifier (skip it, not needed in parsing)
	// Byte index 8-10: Reserved (skip it)
	// Byte index 10: Reference ID Size (Srefid)
	srefid := int(message[10])

	// Byte index 11: Reference ID
	refID := string(message[11 : 11+srefid])

	// Byte after Reference ID: Payload Format
	payloadFormat := message[11+srefid]

	// Byte after Payload Format: Payload Size (4 bytes)
	payloadSizeOffset := 12 + srefid
	payloadSize := binary.LittleEndian.Uint32(message[payloadSizeOffset : payloadSizeOffset+4])

	// Payload
	payloadStart := payloadSizeOffset + 4
	payloadEnd := payloadStart + int(payloadSize)
	payload := message[payloadStart:payloadEnd]

	log.Printf("Received message with Reference ID: %s", refID)
	//log.Printf("Payload Format: %d", payloadFormat)
	log.Printf("Payload Size: %d bytes", payloadSize)

	// Handle control messages or data messages based on Reference ID
	if isControlMessage(refID) {
		handleControlMessage(refID, payload)
	} else {
		handlePayloadMessage(refID, payloadFormat, payload)
	}
}

// isControlMessage determines if a message is a control message
func isControlMessage(refID string) bool {
	switch refID {
	case "_heartbeat", "_disconnect", "_resetsubscriptions":
		return true
	default:
		return false
	}
}

// handleControlMessage processes control messages like heartbeats and disconnects
func handleControlMessage(refID string, payload []byte) {
	switch refID {
	case "_heartbeat":
		log.Printf("Received heartbeat: %v", payload)
	case "_disconnect":
		log.Printf("Received disconnect: %v", payload)
		// Handle disconnect, possibly reconnect or alert user
	case "_resetsubscriptions":
		log.Printf("Received reset subscriptions: %v", payload)
		// Handle reset subscriptions, possibly resubscribe
	default:
		log.Printf("Received unknown control message: %v", payload)
	}
}

// handlePayloadMessage processes price update messages
func handlePayloadMessage(refID string, payloadFormat byte, payload []byte) {
	if payloadFormat != 0 {
		log.Printf("Unexpected payload format: %d. Only JSON format (0) is expected.", payloadFormat)
		return
	}

	var priceUpdate interface{}
	if err := json.Unmarshal(payload, &priceUpdate); err != nil {
		log.Printf("Failed to unmarshal price update message: %v", err)
		return
	}

	// Look up the reference ID in the subscription map to get the subscription type
	if subscriptionType, exists := subscriptionMap[refID]; exists {
		switch subscriptionType {
		case "PriceUpdate":
			switch updates := priceUpdate.(type) {
			case []interface{}:
				log.Printf("Received price update as JSON array: %v", updates)
				for _, item := range updates {
					if itemMap, ok := item.(map[string]interface{}); ok {
						processPriceUpdate(itemMap)
					} else {
						log.Printf("Unexpected item type in price update array: %T", item)
					}
				}
			default:
				log.Printf("Unexpected structure for price update message: %T", updates)
			}
		default:
			log.Printf("Unhandled subscription type: %s", subscriptionType)
		}
	} else {
		log.Printf("Received unknown Reference ID: %s", refID)
	}
}

// Quote represents the quote data structure for FX prices
type Quote struct {
	AskSize float64 `json:"AskSize"`
	BidSize float64 `json:"BidSize"`
	Ask     float64 `json:"Ask"`
	Bid     float64 `json:"Bid"`
	Mid     float64 `json:"Mid"`
}

// PriceUpdate represents the price update data structure
type PriceUpdate struct {
	LastUpdated string `json:"LastUpdated"`
	Quote       Quote  `json:"Quote"`
	Uic         int    `json:"Uic"`
}

// processPriceUpdate processes the price update data
func processPriceUpdate(priceUpdate map[string]interface{}) {
	// Convert the map to JSON bytes
	priceUpdateBytes, err := json.Marshal(priceUpdate)
	if err != nil {
		log.Printf("Failed to marshal price update map: %v", err)
		return
	}

	// Unmarshal JSON bytes into the PriceUpdate struct
	var update PriceUpdate
	if err := json.Unmarshal(priceUpdateBytes, &update); err != nil {
		log.Printf("Failed to unmarshal JSON into PriceUpdate struct: %v", err)
		return
	}

	// Now you can work with the strongly-typed struct
	log.Printf("Processed Price Update: %+v", update)
}

// generateID generates a random ID for the WebSocket connection and subscription's reference ID
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// subscriptionMap stores the reference ID and subscription type mapping
var subscriptionMap = make(map[string]string)

func subcriptionmessage(contextID string, referenceID string, uics []string, assetType string) map[string]interface{} {
	return map[string]interface{}{
		"ContextId":   contextID,
		"ReferenceId": referenceID,
		"RefreshRate": 1000,
		"Arguments": map[string]interface{}{
			"Uics":      strings.Join(uics, ","),
			"AssetType": assetType,
		},
	}
}

// main is the entry point of the program
func main() {
	// Connect to the WebSocket using the obtained access token
	contextID := generateID()
	conn, err := connectWebSocket(accessToken, contextID)
	if err != nil {
		log.Fatalf("Error connecting to WebSocket: %v", err)
	}
	defer conn.Close()

	// Generate a unique reference ID for each subscription and store it in the map
	// Each price update subscription requires a unique reference ID as key, but the same value for type of parsing.
	// Each Instrument category (FxSpot, ContractFutures, etc.) requires a unique subscription, but the same value for type of parsing.

	// Subscribe to fx prices
	referenceID := generateID()
	subscriptionMap[referenceID] = "PriceUpdate"
	fxSpotMessage := subcriptionmessage(contextID, referenceID, fxSpotIDs, "FxSpot")
	err = subscribeToPrices(accessToken, fxSpotMessage)
	if err != nil {
		log.Fatalf("Error subscribing to FxSpot: %v", err)
	}

	// Subscribe to futures
	referenceID = generateID()
	subscriptionMap[referenceID] = "PriceUpdate"
	contractFuturesMessage := subcriptionmessage(contextID, referenceID, contractFuturesIDs, "ContractFutures")
	err = subscribeToPrices(accessToken, contractFuturesMessage)
	if err != nil {
		log.Fatalf("Error subscribing to ContractFutures: %v", err)
	}

	// Handle WebSocket communication
	handleWebSocket(conn)
}
