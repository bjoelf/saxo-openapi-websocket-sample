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

// Define the UICs for the price subscriptions
// For more information on price subscription arguments, see https://www.developer.saxo/openapi/learn/pricing
var (
	fxSpotIDs          = []string{"21", "22"}                         // Modify as needed
	contractFuturesIDs = []string{"37978561", "37978556", "39614794"} // Change for current contracts
)

// Define the field groups for the order subscription
var (
	fieldGroups = []string{
		"DisplayAndFormat",
		"ExchangeInfo",
	}
	activities = []string{
		"Order",
		"Trade",
		"Position",
	}
	accountKey = "yourAccountID" // Replace with your account ID
)

// Replace this with the temporary token provided by Saxo (https://www.developer.saxo/openapi/token/current/)
var accessToken = "" // Temporary access token for testing purposes

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
	timeout := time.After(3 * time.Minute) // Set the timeout for 1 minute

	for {
		select {
		case <-timeout:
			log.Println("Timeout reached, closing connection")
			closeWebSocket(conn)
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

func closeWebSocket(conn *websocket.Conn) {

	// Gracefully close the WebSocket connection
	err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		log.Printf("Error during WebSocket closure: %v", err)
	}

	// Ensure the connection is closed
	err = conn.Close()
	if err != nil {
		log.Printf("Error closing WebSocket: %v", err)
	} else {
		log.Println("WebSocket connection closed successfully.")
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

type HeartbeatMessage struct {
	ReferenceID string `json:"ReferenceId"`
	Heartbeats  []struct {
		OriginatingReferenceID string `json:"OriginatingReferenceId"`
		Reason                 string `json:"Reason"`
	} `json:"Heartbeats"`
}

func parseHeartbeatMessage(message []byte) (*HeartbeatMessage, error) {
	var heartbeat []HeartbeatMessage
	err := json.Unmarshal(message, &heartbeat)
	if err != nil {
		return nil, fmt.Errorf("failed to parse heartbeat message: %v", err)
	}
	return &heartbeat[0], nil
}

type DiconnectMessage struct {
	ReferenceID string `json:"ReferenceId"`
}

func parseDiconnectMessage(message []byte) (*DiconnectMessage, error) {
	var disconnect DiconnectMessage
	err := json.Unmarshal(message, &disconnect)
	if err != nil {
		return nil, fmt.Errorf("failed to parse disconnect message: %v", err)
	}
	return &disconnect, nil
}

type ResetsubscriptionsMessage struct {
	ReferenceID        string   `json:"ReferenceId"`
	TargetReferenceIds []string `json:"TargetReferenceIds"`
}

func parseResetsubscriptionsMessage(message []byte) (*ResetsubscriptionsMessage, error) {
	var resetsubscriptions ResetsubscriptionsMessage
	err := json.Unmarshal(message, &resetsubscriptions)
	if err != nil {
		return nil, fmt.Errorf("failed to parse resetsubscriptions message: %v", err)
	}
	return &resetsubscriptions, nil
}

// handleControlMessage processes control messages like heartbeats and disconnects
func handleControlMessage(refID string, payload []byte) {
	switch refID {
	case "_heartbeat":
		heartbeat, err := parseHeartbeatMessage(payload)
		if err != nil {
			log.Printf("Failed to parse heartbeat message: %v", err)
			return
		}
		log.Printf("Received heartbeat: %s at %s", heartbeat.ReferenceID, time.Now().Format(time.RFC3339))
		log.Printf("Received heartbeat from subscription: %s", subscriptionsMap[heartbeat.Heartbeats[0].OriginatingReferenceID])
		// Handle heartbeat, possibly keep track of last heartbeat time or last message for each subscription
	case "_disconnect":
		disconnect, err := parseDiconnectMessage(payload)
		if err != nil {
			log.Printf("Failed to parse disconnect message: %v", err)
			return
		}
		log.Printf("Received disconnect: %s", disconnect.ReferenceID)
		// Handle disconnect, possibly reconnect or alert user
		log.Printf("log in again and after that recreate the WebSocket connection and set up subscriptions again")
	case "_resetsubscriptions":
		resetsubscriptions, err := parseResetsubscriptionsMessage(payload)
		if err != nil {
			log.Printf("Failed to parse resetsubscriptions message: %v", err)
			return
		}
		log.Printf("Received reset subscriptions: %s", resetsubscriptions.ReferenceID)
		// Handle reset subscriptions, possibly resubscribe
		log.Printf("Resubscribing to: %v", resetsubscriptions.TargetReferenceIds)
	default:
		log.Printf("Received unknown control message: %v", payload)
	}
}

// handlePayloadMessage processes all payload messages
func handlePayloadMessage(refID string, payloadFormat byte, payload []byte) {
	if payloadFormat != 0 {
		log.Printf("Unexpected payload format: %d. Only JSON format (0) is expected.", payloadFormat)
		return
	}

	var streamingUpdate interface{}
	if err := json.Unmarshal(payload, &streamingUpdate); err != nil {
		log.Printf("Failed to unmarshal price update message: %v", err)
		return
	}

	// Look up the reference ID in the subscription map to get the subscription type
	if subscriptionType, exists := subscriptionsMap[refID]; exists {
		switch subscriptionType {
		case "PriceUpdate":
			switch updates := streamingUpdate.(type) {
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
		case "OrderUpdate":
			switch updates := streamingUpdate.(type) {
			case []interface{}:
				log.Printf("Received order update as JSON array: %v", updates)
				for _, item := range updates {
					if itemMap, ok := item.(map[string]interface{}); ok {
						processOrderUpdate(itemMap)
					} else {
						log.Printf("Unexpected item type in order update array: %T", item)
					}
				}
			}
		case "PortfolioBalanceUpdate":
			switch updates := streamingUpdate.(type) {
			case []interface{}:
				log.Printf("Received account update as JSON array: %v", updates)
				for _, item := range updates {
					if itemMap, ok := item.(map[string]interface{}); ok {
						processPortfolioBalance(itemMap)
					} else {
						log.Printf("Unexpected item type in account update array: %T", item)
					}
				}
			}
		default:
			log.Printf("Unhandled subscription type: %s", subscriptionType)
		}
	} else {
		log.Printf("Received unknown Reference ID: %s", refID)
	}
}

func priceSubcriptionMessage(contextID string, referenceID string, uics []string, assetType string) map[string]interface{} {
	return map[string]interface{}{
		"ContextId":   contextID,
		"ReferenceId": referenceID,
		"RefreshRate": 1000,
		"Format":      "application/json", // JSON format test!
		"Arguments": map[string]interface{}{
			"Uics":      strings.Join(uics, ","),
			"AssetType": assetType,
		},
	}
}

func subscribeToPrices(accessToken string, subscription map[string]interface{}) error {

	// Convert subscription data to JSON
	subscriptionData, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Errorf("failed to marshal price subscription data: %v", err)
	}

	// Create the HTTP POST request
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/trade/v1/infoprices/subscriptions", apiBaseURL), bytes.NewBuffer(subscriptionData))
	if err != nil {
		return fmt.Errorf("failed to create price request: %v", err)
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
		return fmt.Errorf("failed to send price request: %v", err)
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

type priceQuote struct {
	AskSize float64 `json:"AskSize"`
	BidSize float64 `json:"BidSize"`
	Ask     float64 `json:"Ask"`
	Bid     float64 `json:"Bid"`
	Mid     float64 `json:"Mid"`
}

type PriceUpdate struct {
	LastUpdated string     `json:"LastUpdated"`
	Quote       priceQuote `json:"Quote"`
	Uic         int        `json:"Uic"`
}

func processPriceUpdate(streamingUpdate map[string]interface{}) {
	// Convert the map to JSON bytes
	priceUpdateBytes, err := json.Marshal(streamingUpdate)
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

// https://www.developer.saxo/openapi/referencedocs/port/v1/orders/post__port__subscriptions
func orderSubcriptionMessage(contextID string, referenceID string, accountKey string) map[string]interface{} {
	return map[string]interface{}{
		"ContextId":   contextID,
		"ReferenceId": referenceID,
		"Format":      "application/json",
		"RefreshRate": 5,
		"Arguments": map[string]interface{}{
			"AccountKey":      accountKey,
			"AccountGroupKey": accountKey,
			"ClientKey":       accountKey,
		},
	}
}

func subscribeToOrders(accessToken string, subscription map[string]interface{}) error {
	// Convert subscription data to JSON
	subscriptionData, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Errorf("failed to marshal order subscription data: %v", err)
	}
	// Create the HTTP POST request
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/port/v1/orders/subscriptions", apiBaseURL), bytes.NewBuffer(subscriptionData))
	if err != nil {
		return fmt.Errorf("failed to create order request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Log the full request for debugging
	log.Printf("Subscription Request URL: %s", req.URL.String())
	log.Printf("Subscription Request Body: %s", subscriptionData)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send order request: %v", err)
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

type OrderUpdate struct {
	AccountID                string  `json:"AccountId"`
	AccountKey               string  `json:"AccountKey"`
	Amount                   int     `json:"Amount"`
	AssetType                string  `json:"AssetType"`
	BuySell                  string  `json:"BuySell"`
	CalculationReliability   string  `json:"CalculationReliability"`
	ClientKey                string  `json:"ClientKey"`
	CurrentPrice             float64 `json:"CurrentPrice"`
	CurrentPriceDelayMinutes int     `json:"CurrentPriceDelayMinutes"`
	CurrentPriceType         string  `json:"CurrentPriceType"`
	DistanceToMarket         float64 `json:"DistanceToMarket"`
	Duration                 struct {
		DurationType string `json:"DurationType"`
	} `json:"Duration"`
	IsForceOpen       bool      `json:"IsForceOpen"`
	IsMarketOpen      bool      `json:"IsMarketOpen"`
	MarketPrice       float64   `json:"MarketPrice"`
	NonTradableReason string    `json:"NonTradableReason"`
	OpenOrderType     string    `json:"OpenOrderType"`
	OrderAmountType   string    `json:"OrderAmountType"`
	OrderID           string    `json:"OrderId"`
	OrderRelation     string    `json:"OrderRelation"`
	OrderTime         time.Time `json:"OrderTime"`
	Price             float64   `json:"Price"`
	Status            string    `json:"Status"`
	Uic               int       `json:"Uic"`
}

func processOrderUpdate(orderUpdate map[string]interface{}) {

	// Convert the map to JSON bytes
	orderUpdateBytes, err := json.Marshal(orderUpdate)
	if err != nil {
		log.Printf("Failed to marshal order update map: %v", err)
		return
	}

	// Unmarshal JSON bytes into the OrderUpdate struct
	var update OrderUpdate
	if err := json.Unmarshal(orderUpdateBytes, &update); err != nil {
		log.Printf("Failed to unmarshal JSON into OrderUpdate struct: %v", err)
		return
	}

	// Now you can work with the strongly-typed struct
	log.Printf("Processed Order Update: %+v", update)
}

// https://www.developer.saxo/openapi/referencedocs/port/v1/balances/post__port__subscriptions
func portfolioBalanceSubscriptionMessage(contextID string, referenceID string, accountKey string) map[string]interface{} {
	return map[string]interface{}{
		"ContextId":   contextID,
		"ReferenceId": referenceID,
		"Format":      "application/json",
		"RefreshRate": 500,
		"Tag":         "MyBalancesRelatedSubscriptions",
		"Arguments": map[string]interface{}{
			"AccountGroupKey": accountKey,
			"AccountKey":      accountKey,
			"ClientKey":       accountKey,
		},
	}
}

func subscribeToPortfolioBalance(accessToken string, subscription map[string]interface{}) error {
	// Convert subscription data to JSON
	subscriptionData, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Errorf("failed to marshal account subscription data: %v", err)
	}
	// Create the HTTP POST request
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/port/v1/balances/subscriptions", apiBaseURL), bytes.NewBuffer(subscriptionData))
	if err != nil {
		return fmt.Errorf("failed to create account request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Log the full request for debugging
	log.Printf("Subscription Request URL: %s", req.URL.String())
	log.Printf("Subscription Request Body: %s", subscriptionData)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send account request: %v", err)
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

type PortfolioBalanceUpdate struct {
	CalculationReliability           string  `json:"CalculationReliability"`
	CashAvailableForTrading          float64 `json:"CashAvailableForTrading"`
	CashBalance                      float64 `json:"CashBalance"`
	CashBlocked                      int     `json:"CashBlocked"`
	ChangesScheduled                 bool    `json:"ChangesScheduled"`
	ClosedPositionsCount             int     `json:"ClosedPositionsCount"`
	CollateralAvailable              float64 `json:"CollateralAvailable"`
	CorporateActionUnrealizedAmounts int     `json:"CorporateActionUnrealizedAmounts"`
	CostToClosePositions             float64 `json:"CostToClosePositions"`
	Currency                         string  `json:"Currency"`
	CurrencyDecimals                 int     `json:"CurrencyDecimals"`
	InitialMargin                    struct {
		CollateralAvailable          float64 `json:"CollateralAvailable"`
		MarginAvailable              float64 `json:"MarginAvailable"`
		MarginCollateralNotAvailable int     `json:"MarginCollateralNotAvailable"`
		MarginUsedByCurrentPositions float64 `json:"MarginUsedByCurrentPositions"`
		MarginUtilizationPct         float64 `json:"MarginUtilizationPct"`
		NetEquityForMargin           float64 `json:"NetEquityForMargin"`
		OtherCollateralDeduction     int     `json:"OtherCollateralDeduction"`
	} `json:"InitialMargin"`
	IntradayMarginDiscount            int     `json:"IntradayMarginDiscount"`
	IsPortfolioMarginModelSimple      bool    `json:"IsPortfolioMarginModelSimple"`
	MarginAndCollateralUtilizationPct float64 `json:"MarginAndCollateralUtilizationPct"`
	MarginAvailableForTrading         float64 `json:"MarginAvailableForTrading"`
	MarginCollateralNotAvailable      float64 `json:"MarginCollateralNotAvailable"`
	MarginExposureCoveragePct         float64 `json:"MarginExposureCoveragePct"`
	MarginNetExposure                 float64 `json:"MarginNetExposure"`
	MarginUsedByCurrentPositions      float64 `json:"MarginUsedByCurrentPositions"`
	MarginUtilizationPct              float64 `json:"MarginUtilizationPct"`
	NetEquityForMargin                float64 `json:"NetEquityForMargin"`
	NetPositionsCount                 int     `json:"NetPositionsCount"`
	NonMarginPositionsValue           float64 `json:"NonMarginPositionsValue"`
	OpenIpoOrdersCount                int     `json:"OpenIpoOrdersCount"`
	OpenPositionsCount                int     `json:"OpenPositionsCount"`
	OptionPremiumsMarketValue         int     `json:"OptionPremiumsMarketValue"`
	OrdersCount                       int     `json:"OrdersCount"`
	OtherCollateral                   float64 `json:"OtherCollateral"`
	SettlementValue                   int     `json:"SettlementValue"`
	SpendingPowerDetail               struct {
		Current int `json:"Current"`
		Maximum int `json:"Maximum"`
	} `json:"SpendingPowerDetail"`
	TotalValue                       float64 `json:"TotalValue"`
	TransactionsNotBooked            float64 `json:"TransactionsNotBooked"`
	TriggerOrdersCount               int     `json:"TriggerOrdersCount"`
	UnrealizedMarginClosedProfitLoss int     `json:"UnrealizedMarginClosedProfitLoss"`
	UnrealizedMarginOpenProfitLoss   int     `json:"UnrealizedMarginOpenProfitLoss"`
	UnrealizedMarginProfitLoss       float64 `json:"UnrealizedMarginProfitLoss"`
	UnrealizedPositionsValue         float64 `json:"UnrealizedPositionsValue"`
}

func processPortfolioBalance(portfolioBalance map[string]interface{}) {

	// Convert the map to JSON bytes
	accountUpdateBytes, err := json.Marshal(portfolioBalance)
	if err != nil {
		log.Printf("Failed to marshal account update map: %v", err)
		return
	}

	// Unmarshal JSON bytes into the PortfolioBalance struct
	var update PortfolioBalanceUpdate
	if err := json.Unmarshal(accountUpdateBytes, &update); err != nil {
		log.Printf("Failed to unmarshal JSON into AccountUpdate struct: %v", err)
		return
	}

	log.Printf("Processed Portfolio Balance: %+v", update)
}

// subscriptionsMap stores the reference ID and subscription type mapping
var subscriptionsMap = make(map[string]string)

// generateID generates a random ID for the WebSocket connection and subscription's reference ID
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
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

	// Generate a unique reference ID for each subscription and store it in the subscriptionsMap
	// Each instrument type needs a separate price subscription that requires a unique reference ID as key, but the same value for type of parsing.
	// Each Instrument category (FxSpot, ContractFutures, etc.) requires a unique subscription, but the same value for type of parsing.
	// subscriptionsMap routes the parsing of received messages in the handleMessage function based on the reference ID

	// Subscribe to fx prices
	referenceID := generateID()
	subscriptionsMap[referenceID] = "PriceUpdate"
	fxSpotMessage := priceSubcriptionMessage(contextID, referenceID, fxSpotIDs, "FxSpot")
	err = subscribeToPrices(accessToken, fxSpotMessage)
	if err != nil {
		closeWebSocket(conn)
		log.Fatalf("Error subscribing to FxSpot: %v", err)
	}

	// Subscribe to futures prices
	referenceID = generateID()
	subscriptionsMap[referenceID] = "PriceUpdate"
	contractFuturesMessage := priceSubcriptionMessage(contextID, referenceID, contractFuturesIDs, "ContractFutures")
	err = subscribeToPrices(accessToken, contractFuturesMessage)
	if err != nil {
		closeWebSocket(conn)
		log.Fatalf("Error subscribing to ContractFutures: %v", err)
	}

	// Subscribe to orders
	referenceID = generateID()
	subscriptionsMap[referenceID] = "OrderUpdate"

	orderMessage := orderSubcriptionMessage(contextID, referenceID, accountKey)
	err = subscribeToOrders(accessToken, orderMessage)
	if err != nil {
		closeWebSocket(conn)
		log.Fatalf("Error subscribing to Orders: %v", err)
	}

	// Subscribe to account
	referenceID = generateID()
	subscriptionsMap[referenceID] = "PortfolioBalanceUpdate"
	accountMessage := portfolioBalanceSubscriptionMessage(contextID, referenceID, accountKey)
	err = subscribeToPortfolioBalance(accessToken, accountMessage)
	if err != nil {
		closeWebSocket(conn)
		log.Fatalf("Error subscribing to Account: %v", err)
	}

	// Handle WebSocket communication
	handleWebSocket(conn)
}
