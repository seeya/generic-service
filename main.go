package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/PuerkitoBio/goquery"
)

type Config struct {
	Stages []Stage      `toml:"stages"`
	Server ServerConfig `toml:"server"`
}

// ServerConfig holds server configuration
type ServerConfig struct {
	Port string `toml:"port"`
	Host string `toml:"host"`
}

// Stage represents a single action stage
type Stage struct {
	Name       string                 `toml:"name"`
	Action     string                 `toml:"action"`
	Parameters map[string]interface{} `toml:"parameters"`
}

// Context holds data that can be shared between stages
type Context struct {
	Variables map[string]interface{} `json:"variables"`
	Outputs   map[string]interface{} `json:"outputs"`
}

// APIEndpoint represents an API endpoint configuration
type APIEndpoint struct {
	Path        string
	Method      string
	Pipeline    []Stage
	Description string
}

// Service handles the execution of stages
type Service struct {
	config    Config
	context   *Context
	client    *http.Client
	endpoints map[string]*APIEndpoint
	mux       *http.ServeMux
}

// HTTPResponse represents the response from HTTP actions
type HTTPResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       interface{}         `json:"body"`
	RawBody    string              `json:"raw_body"`
}

// APIRequest represents the structure of incoming API requests
type APIRequest struct {
	Variables map[string]interface{} `json:"variables"`
	Data      map[string]interface{} `json:"data"`
}

// APIResponse represents the structure of API responses
type APIResponse struct {
	Success bool                   `json:"success"`
	Data    interface{}            `json:"data,omitempty"`
	Error   string                 `json:"error,omitempty"`
	Context map[string]interface{} `json:"context,omitempty"`
}

// NewService creates a new service instance
func NewService(configPath string) (*Service, error) {
	var config Config
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	// Set default server config
	if config.Server.Port == "" {
		config.Server.Port = "8080"
	}
	if config.Server.Host == "" {
		config.Server.Host = "localhost"
	}

	service := &Service{
		config: config,
		context: &Context{
			Variables: make(map[string]interface{}),
			Outputs:   make(map[string]interface{}),
		},
		client:    &http.Client{Timeout: 600 * time.Second},
		endpoints: make(map[string]*APIEndpoint),
		mux:       http.NewServeMux(),
	}

	// Register built-in endpoints
	service.registerBuiltinEndpoints()

	return service, nil
}

// registerBuiltinEndpoints registers system endpoints
func (s *Service) registerBuiltinEndpoints() {
	// Health check endpoint
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		s.sendJSONResponse(w, APIResponse{
			Success: true,
			Data:    map[string]string{"status": "healthy"},
		})
	})

	// List endpoints
	s.mux.HandleFunc("GET /endpoints", func(w http.ResponseWriter, r *http.Request) {
		endpoints := make([]map[string]string, 0, len(s.endpoints))
		for _, endpoint := range s.endpoints {
			endpoints = append(endpoints, map[string]string{
				"path":        endpoint.Path,
				"method":      endpoint.Method,
				"description": endpoint.Description,
			})
		}
		s.sendJSONResponse(w, APIResponse{
			Success: true,
			Data:    endpoints,
		})
	})
}

// StartServer starts the HTTP server and processes API stages
func (s *Service) StartServer() error {
	// First, process all stages to set up API endpoints
	if err := s.setupAPIEndpoints(); err != nil {
		return fmt.Errorf("failed to setup API endpoints: %w", err)
	}

	addr := fmt.Sprintf("%s:%s", s.config.Server.Host, s.config.Server.Port)
	log.Printf("Starting server on %s", addr)
	log.Printf("Registered %d API endpoints", len(s.endpoints))

	return http.ListenAndServe(addr, s.mux)
}

// setupAPIEndpoints processes stages and creates API endpoints
func (s *Service) setupAPIEndpoints() error {
	var apiStages []Stage
	var currentPipeline []Stage

	for _, stage := range s.config.Stages {
		if strings.ToLower(stage.Action) == "api" {
			// If we have accumulated stages, create an endpoint
			if len(currentPipeline) > 0 {
				if err := s.createAPIEndpoint(stage, currentPipeline); err != nil {
					return fmt.Errorf("failed to create API endpoint for stage '%s': %w", stage.Name, err)
				}
				currentPipeline = nil
			}
			apiStages = append(apiStages, stage)
		} else {
			// Accumulate non-API stages
			currentPipeline = append(currentPipeline, stage)
		}
	}

	// If there are remaining stages without an API definition, create a default endpoint
	if len(currentPipeline) > 0 {
		defaultAPIStage := Stage{
			Name:   "default_api",
			Action: "api",
			Parameters: map[string]interface{}{
				"path":   "/execute",
				"method": "POST",
			},
		}
		if err := s.createAPIEndpoint(defaultAPIStage, currentPipeline); err != nil {
			return fmt.Errorf("failed to create default API endpoint: %w", err)
		}
	}

	return nil
}

// createAPIEndpoint creates an HTTP endpoint for a pipeline
func (s *Service) createAPIEndpoint(apiStage Stage, pipeline []Stage) error {
	path := getStringParam(apiStage.Parameters, "path")
	if path == "" {
		return fmt.Errorf("API stage requires 'path' parameter")
	}

	method := strings.ToUpper(getStringParam(apiStage.Parameters, "method"))
	if method == "" {
		method = "POST"
	}

	description := getStringParam(apiStage.Parameters, "description")
	if description == "" {
		description = fmt.Sprintf("Auto-generated endpoint for %s", apiStage.Name)
	}

	endpoint := &APIEndpoint{
		Path:        path,
		Method:      method,
		Pipeline:    pipeline,
		Description: description,
	}

	// Register the endpoint
	pattern := fmt.Sprintf("%s %s", method, path)
	s.mux.HandleFunc(pattern, s.createEndpointHandler(endpoint))

	s.endpoints[apiStage.Name] = endpoint
	log.Printf("Registered endpoint: %s %s", method, path)

	return nil
}

// createEndpointHandler creates an HTTP handler for an endpoint
func (s *Service) createEndpointHandler(endpoint *APIEndpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Create a new context for this request
		requestContext := &Context{
			Variables: make(map[string]interface{}),
			Outputs:   make(map[string]interface{}),
		}

		// Parse request body
		if r.Body != nil {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				s.sendErrorResponse(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusBadRequest)
				return
			}

			if len(bodyBytes) > 0 {
				var apiRequest APIRequest
				if err := json.Unmarshal(bodyBytes, &apiRequest); err != nil {
					s.sendErrorResponse(w, fmt.Sprintf("Invalid JSON in request body: %v", err), http.StatusBadRequest)
					return
				}

				// Set variables from request
				if apiRequest.Variables != nil {
					for k, v := range apiRequest.Variables {
						requestContext.Variables[k] = v
					}
				}

				// Set data as a special variable
				if apiRequest.Data != nil {
					requestContext.Variables["request_data"] = apiRequest.Data
				}
			}
		}

		// Add query parameters as variables
		for key, values := range r.URL.Query() {
			if len(values) == 1 {
				requestContext.Variables["query_"+key] = values[0]
			} else {
				requestContext.Variables["query_"+key] = values
			}
		}

		// Add path parameters (if any) - for future enhancement
		requestContext.Variables["request_path"] = r.URL.Path
		requestContext.Variables["request_method"] = r.Method

		// Execute the pipeline
		log.Printf("Pipelines: %v", endpoint.Pipeline)
		result, err := s.executePipeline(endpoint.Pipeline, requestContext)
		if err != nil {
			s.sendErrorResponse(w, fmt.Sprintf("Pipeline execution failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Send successful response
		s.sendJSONResponse(w, APIResponse{
			Success: true,
			Data:    result,
			Context: requestContext.Variables,
		})
	}
}

// executePipeline executes a series of stages with a given context
func (s *Service) executePipeline(pipeline []Stage, ctx *Context) (interface{}, error) {
	// Create a temporary service instance with the request context
	tempService := &Service{
		config:  s.config,
		context: ctx,
		client:  s.client,
	}

	var lastResult interface{}

	for i, stage := range pipeline {
		log.Printf("Executing pipeline stage %d: %s (%s)", i+1, stage.Name, stage.Action)

		result, err := tempService.executeStage(stage)
		if err != nil {
			return nil, fmt.Errorf("stage '%s' failed: %w", stage.Name, err)
		}

		// Store the result in context
		ctx.Outputs[stage.Name] = result
		lastResult = result

		log.Printf("Pipeline stage %s completed successfully", stage.Name)
	}

	return lastResult, nil
}

// sendJSONResponse sends a JSON response
func (s *Service) sendJSONResponse(w http.ResponseWriter, response APIResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}

// sendErrorResponse sends an error response
func (s *Service) sendErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	response := APIResponse{
		Success: false,
		Error:   message,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Failed to encode error response: %v", err)
	}
}

// ScrapeElement represents a scraped HTML element
type ScrapeElement struct {
	Text       string            `json:"text"`
	HTML       string            `json:"html"`
	Attributes map[string]string `json:"attributes"`
}

// executeScrape performs HTML scraping operations
func (s *Service) executeScrape(stage Stage) (interface{}, error) {
	// Get HTML content from input
	inputKey := getStringParam(stage.Parameters, "input")
	if inputKey == "" {
		return nil, fmt.Errorf("scrape requires 'input' parameter")
	}

	var htmlContent string

	// Handle different input sources
	if strings.HasPrefix(inputKey, "stages.") {
		// Reference to previous stage output
		stageRef := strings.TrimPrefix(inputKey, "stages.")
		parts := strings.SplitN(stageRef, ".", 2)
		if len(parts) < 1 {
			return nil, fmt.Errorf("invalid stage reference: %s", inputKey)
		}

		stageOutput, exists := s.context.Outputs[parts[0]]
		if !exists {
			return nil, fmt.Errorf("stage output not found: %s", parts[0])
		}

		if len(parts) == 1 {
			// Use entire stage output
			if httpResp, ok := stageOutput.(*HTTPResponse); ok {
				htmlContent = httpResp.RawBody
			} else {
				htmlContent = fmt.Sprintf("%v", stageOutput)
			}
		} else {
			// Use specific field from stage output
			data := s.getNestedValue(stageOutput, parts[1])
			htmlContent = fmt.Sprintf("%v", data)
		}
	} else if strings.HasPrefix(inputKey, "variables.") {
		// Reference to variable
		varName := strings.TrimPrefix(inputKey, "variables.")
		var exists bool
		varData, exists := s.context.Variables[varName]
		if !exists {
			return nil, fmt.Errorf("variable not found: %s", varName)
		}
		htmlContent = fmt.Sprintf("%v", varData)
	} else {
		// Direct HTML content
		var err error
		htmlContent, err = s.interpolateString(inputKey)
		if err != nil {
			return nil, fmt.Errorf("failed to interpolate HTML content: %w", err)
		}
	}

	// Parse HTML
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Get CSS selector
	selector := getStringParam(stage.Parameters, "selector")
	if selector == "" {
		return nil, fmt.Errorf("scrape requires 'selector' parameter")
	}

	// Interpolate selector in case it contains variables
	selector, err = s.interpolateString(selector)
	if err != nil {
		return nil, fmt.Errorf("failed to interpolate selector: %w", err)
	}

	// Find elements
	selection := doc.Find(selector)
	if selection.Length() == 0 {
		return []interface{}{}, nil
	}

	// Apply operations
	operation := getStringParam(stage.Parameters, "operation")
	switch operation {
	case "filter":
		return s.applyScrapeFilter(selection, stage.Parameters)
	case "map":
		return s.applyScrapeMap(selection, stage.Parameters)
	case "first":
		return s.getFirstElement(selection, stage.Parameters)
	case "last":
		return s.getLastElement(selection, stage.Parameters)
	case "index":
		return s.getElementByIndex(selection, stage.Parameters)
	case "count":
		return selection.Length(), nil
	case "extract":
		return s.extractFromElements(selection, stage.Parameters)
	default:
		// Default: return all elements
		return s.selectionToElements(selection), nil
	}
}

// applyScrapeFilter filters elements based on conditions
func (s *Service) applyScrapeFilter(selection *goquery.Selection, params map[string]interface{}) (interface{}, error) {
	filterType := getStringParam(params, "filter_type")
	filterValue := getStringParam(params, "filter_value")

	var filtered *goquery.Selection

	switch filterType {
	case "attribute":
		attrName := getStringParam(params, "attribute")
		if attrName == "" {
			return nil, fmt.Errorf("filter by attribute requires 'attribute' parameter")
		}

		if filterValue == "" {
			// Filter elements that have the attribute
			filtered = selection.FilterFunction(func(i int, s *goquery.Selection) bool {
				_, exists := s.Attr(attrName)
				return exists
			})
		} else {
			// Filter elements where attribute contains/equals value
			contains := getBoolParam(params, "contains", true)
			filtered = selection.FilterFunction(func(i int, s *goquery.Selection) bool {
				attrVal, exists := s.Attr(attrName)
				if !exists {
					return false
				}
				if contains {
					return strings.Contains(attrVal, filterValue)
				}
				return attrVal == filterValue
			})
		}

	case "text":
		contains := getBoolParam(params, "contains", true)
		filtered = selection.FilterFunction(func(i int, s *goquery.Selection) bool {
			text := strings.TrimSpace(s.Text())
			if contains {
				return strings.Contains(text, filterValue)
			}
			return text == filterValue
		})

	case "regex":
		regex, err := regexp.Compile(filterValue)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern: %w", err)
		}

		target := getStringParam(params, "target") // "text" or attribute name
		if target == "" || target == "text" {
			filtered = selection.FilterFunction(func(i int, s *goquery.Selection) bool {
				return regex.MatchString(s.Text())
			})
		} else {
			filtered = selection.FilterFunction(func(i int, s *goquery.Selection) bool {
				attrVal, exists := s.Attr(target)
				return exists && regex.MatchString(attrVal)
			})
		}

	default:
		return nil, fmt.Errorf("unsupported filter_type: %s", filterType)
	}

	return s.selectionToElements(filtered), nil
}

// applyScrapeMap transforms each element
func (s *Service) applyScrapeMap(selection *goquery.Selection, params map[string]interface{}) (interface{}, error) {
	mapType := getStringParam(params, "map_type")

	var results []interface{}

	selection.Each(func(i int, s *goquery.Selection) {
		switch mapType {
		case "text":
			results = append(results, strings.TrimSpace(s.Text()))
		case "html":
			html, _ := s.Html()
			results = append(results, html)
		case "attribute":
			attrName := getStringParam(params, "attribute")
			if attrName != "" {
				if attrVal, exists := s.Attr(attrName); exists {
					results = append(results, attrVal)
				} else {
					results = append(results, nil)
				}
			}
		case "custom":
			// Allow custom extraction with multiple fields
			element := make(map[string]interface{})

			if extractAttrs, ok := params["extract_attributes"].([]interface{}); ok {
				attrs := make(map[string]string)
				for _, attr := range extractAttrs {
					if attrName, ok := attr.(string); ok {
						if attrVal, exists := s.Attr(attrName); exists {
							attrs[attrName] = attrVal
						}
					}
				}
				element["attributes"] = attrs
			}

			if getBoolParam(params, "include_text", false) {
				element["text"] = strings.TrimSpace(s.Text())
			}

			if getBoolParam(params, "include_html", false) {
				html, _ := s.Html()
				element["html"] = html
			}

			results = append(results, element)
		}
	})

	return results, nil
}

// getFirstElement returns the first element
func (s *Service) getFirstElement(selection *goquery.Selection, params map[string]interface{}) (interface{}, error) {
	if selection.Length() == 0 {
		return nil, nil
	}

	first := selection.First()
	return s.elementToMap(first), nil
}

// getLastElement returns the last element
func (s *Service) getLastElement(selection *goquery.Selection, params map[string]interface{}) (interface{}, error) {
	if selection.Length() == 0 {
		return nil, nil
	}

	last := selection.Last()
	return s.elementToMap(last), nil
}

// getElementByIndex returns element at specific index
func (s *Service) getElementByIndex(selection *goquery.Selection, params map[string]interface{}) (interface{}, error) {
	indexParam := params["index"]
	var index int

	switch v := indexParam.(type) {
	case int:
		index = v
	case int64:
		index = int(v)
	case string:
		var err error
		index, err = strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid index: %s", v)
		}
	default:
		return nil, fmt.Errorf("index must be a number")
	}

	if index < 0 || index >= selection.Length() {
		return nil, nil
	}

	element := selection.Eq(index)
	return s.elementToMap(element), nil
}

// extractFromElements extracts specific data from all elements
func (s *Service) extractFromElements(selection *goquery.Selection, params map[string]interface{}) (interface{}, error) {
	extractType := getStringParam(params, "extract_type")

	switch extractType {
	case "urls":
		// Extract all URLs from href attributes
		var urls []string
		selection.Each(func(i int, s *goquery.Selection) {
			if href, exists := s.Attr("href"); exists {
				urls = append(urls, href)
			}
		})
		return urls, nil

	case "images":
		// Extract all image sources
		var images []string
		selection.Each(func(i int, s *goquery.Selection) {
			if src, exists := s.Attr("src"); exists {
				images = append(images, src)
			}
		})
		return images, nil

	case "text_list":
		// Extract all text content
		var texts []string
		selection.Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if text != "" {
				texts = append(texts, text)
			}
		})
		return texts, nil

	default:
		return nil, fmt.Errorf("unsupported extract_type: %s", extractType)
	}
}

// selectionToElements converts a selection to a slice of elements
func (s *Service) selectionToElements(selection *goquery.Selection) []interface{} {
	var elements []interface{}

	selection.Each(func(i int, sel *goquery.Selection) {
		elements = append(elements, s.elementToMap(sel))
	})

	return elements
}

// elementToMap converts a single element to a map
func (s *Service) elementToMap(selection *goquery.Selection) map[string]interface{} {
	element := make(map[string]interface{})

	// Get text content
	element["text"] = strings.TrimSpace(selection.Text())

	// Get HTML content
	html, _ := selection.Html()
	element["html"] = html

	// Get all attributes
	attrs := make(map[string]string)
	selection.Each(func(i int, s *goquery.Selection) {
		// Get the underlying node to access all attributes
		if s.Nodes != nil && len(s.Nodes) > 0 {
			for _, attr := range s.Nodes[0].Attr {
				attrs[attr.Key] = attr.Val
			}
		}
	})
	element["attributes"] = attrs

	return element
}

// Execute runs all stages in sequence (legacy mode)
func (s *Service) Execute() error {
	for i, stage := range s.config.Stages {
		fmt.Printf("Executing stage %d: %s (%s)\n", i+1, stage.Name, stage.Action)

		output, err := s.executeStage(stage)
		if err != nil {
			return fmt.Errorf("stage '%s' failed: %w", stage.Name, err)
		}

		// Store the output in context for future stages
		s.context.Outputs[stage.Name] = output

		fmt.Printf("Stage %s completed successfully\n", stage.Name)
	}
	return nil
}

// executeStage executes a single stage based on its action type
func (s *Service) executeStage(stage Stage) (interface{}, error) {
	switch strings.ToLower(stage.Action) {
	case "get":
		return s.executeHTTPGet(stage)
	case "post":
		return s.executeHTTPPost(stage)
	case "transform":
		return s.executeTransform(stage)
	case "download":
		return s.executeDownload(stage)
	case "set_variable":
		return s.executeSetVariable(stage)
	case "scrape":
		return s.executeScrape(stage)
	case "api":
		// API stages are handled during server setup, not execution
		return map[string]interface{}{
			"type":   "api_endpoint",
			"path":   getStringParam(stage.Parameters, "path"),
			"method": getStringParam(stage.Parameters, "method"),
		}, nil
	default:
		return nil, fmt.Errorf("unknown action: %s", stage.Action)
	}
}

// executeHTTPGet performs a GET request
func (s *Service) executeHTTPGet(stage Stage) (*HTTPResponse, error) {
	url, err := s.interpolateString(getStringParam(stage.Parameters, "url"))
	if err != nil {
		return nil, fmt.Errorf("failed to interpolate URL: %w", err)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers if specified
	if headers, ok := stage.Parameters["headers"].(map[string]interface{}); ok {
		for key, value := range headers {
			headerValue, err := s.interpolateString(fmt.Sprintf("%v", value))
			if err != nil {
				return nil, fmt.Errorf("failed to interpolate header %s: %w", key, err)
			}
			req.Header.Set(key, headerValue)
		}
	}

	return s.executeHTTPRequest(req)
}

// executeHTTPPost performs a POST request
func (s *Service) executeHTTPPost(stage Stage) (*HTTPResponse, error) {
	url, err := s.interpolateString(getStringParam(stage.Parameters, "url"))
	if err != nil {
		return nil, fmt.Errorf("failed to interpolate URL: %w", err)
	}

	var body io.Reader
	contentType := "application/json"

	if bodyData, ok := stage.Parameters["body"]; ok {
		switch v := bodyData.(type) {
		case string:
			interpolated, err := s.interpolateString(v)
			if err != nil {
				return nil, fmt.Errorf("failed to interpolate body: %w", err)
			}
			body = strings.NewReader(interpolated)
		case map[string]interface{}:
			jsonData, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal JSON body: %w", err)
			}
			body = bytes.NewReader(jsonData)
		default:
			return nil, fmt.Errorf("unsupported body type: %T", v)
		}
	}

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)

	// Add headers if specified
	if headers, ok := stage.Parameters["headers"].(map[string]interface{}); ok {
		for key, value := range headers {
			headerValue, err := s.interpolateString(fmt.Sprintf("%v", value))
			if err != nil {
				return nil, fmt.Errorf("failed to interpolate header %s: %w", key, err)
			}
			req.Header.Set(key, headerValue)
		}
	}

	return s.executeHTTPRequest(req)
}

// executeHTTPRequest executes an HTTP request and returns the response
func (s *Service) executeHTTPRequest(req *http.Request) (*HTTPResponse, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	rawBody := string(bodyBytes)

	// Try to parse as JSON, fall back to raw string
	var bodyData interface{}
	if err := json.Unmarshal(bodyBytes, &bodyData); err != nil {
		bodyData = rawBody
	}

	return &HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       bodyData,
		RawBody:    rawBody,
	}, nil
}

// executeTransform applies transformations to data
func (s *Service) executeTransform(stage Stage) (interface{}, error) {
	inputKey := getStringParam(stage.Parameters, "input")
	if inputKey == "" {
		return nil, fmt.Errorf("transform requires 'input' parameter")
	}

	// Get input data from context
	var inputData interface{}
	if strings.HasPrefix(inputKey, "stages.") {
		// Reference to previous stage output
		stageRef := strings.TrimPrefix(inputKey, "stages.")
		parts := strings.SplitN(stageRef, ".", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid stage reference: %s", inputKey)
		}

		stageOutput, exists := s.context.Outputs[parts[0]]
		if !exists {
			return nil, fmt.Errorf("stage output not found: %s", parts[0])
		}

		inputData = s.getNestedValue(stageOutput, parts[1])
	} else if strings.HasPrefix(inputKey, "variables.") {
		// Reference to variable
		varName := strings.TrimPrefix(inputKey, "variables.")
		var exists bool
		inputData, exists = s.context.Variables[varName]
		if !exists {
			return nil, fmt.Errorf("variable not found: %s", varName)
		}
	}

	// Apply transformations
	transformType := getStringParam(stage.Parameters, "type")
	switch transformType {
	case "json_extract":
		path := getStringParam(stage.Parameters, "path")
		return s.extractJSONPath(inputData, path)
	case "template":
		templateStr := getStringParam(stage.Parameters, "template")
		return s.applyTemplate(templateStr, inputData)
	case "regex":
		pattern := getStringParam(stage.Parameters, "pattern")
		replacement := getStringParam(stage.Parameters, "replacement")
		return s.applyRegex(inputData, pattern, replacement)
	default:
		return nil, fmt.Errorf("unknown transform type: %s", transformType)
	}
}

// executeDownload downloads a file
func (s *Service) executeDownload(stage Stage) (interface{}, error) {
	url, err := s.interpolateString(getStringParam(stage.Parameters, "url"))
	if err != nil {
		return nil, fmt.Errorf("failed to interpolate URL: %w", err)
	}

	outputPath, err := s.interpolateString(getStringParam(stage.Parameters, "output"))
	if err != nil {
		return nil, fmt.Errorf("failed to interpolate output path: %w", err)
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Download file
	resp, err := s.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// Create output file
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Copy content
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return map[string]interface{}{
		"file_path": outputPath,
		"status":    "downloaded",
	}, nil
}

// executeSetVariable sets a variable in the context
func (s *Service) executeSetVariable(stage Stage) (interface{}, error) {
	name := getStringParam(stage.Parameters, "name")
	if name == "" {
		return nil, fmt.Errorf("set_variable requires 'name' parameter")
	}

	value := stage.Parameters["value"]
	if valueStr, ok := value.(string); ok {
		interpolated, err := s.interpolateString(valueStr)
		if err != nil {
			return nil, fmt.Errorf("failed to interpolate value: %w", err)
		}
		value = interpolated
	}

	s.context.Variables[name] = value
	return map[string]interface{}{
		"variable": name,
		"value":    value,
	}, nil
}

// interpolateString replaces variables in a string with their values
func (s *Service) interpolateString(str string) (string, error) {
	tmpl, err := template.New("interpolate").Parse(str)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	data := map[string]interface{}{
		"variables": s.context.Variables,
		"stages":    s.context.Outputs,
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// getNestedValue extracts a nested value from a data structure using dot notation
func (s *Service) getNestedValue(data interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	current := data

	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			current = v[part]
		case *HTTPResponse:
			switch part {
			case "status_code":
				current = v.StatusCode
			case "headers":
				current = v.Headers
			case "body":
				current = v.Body
			case "raw_body":
				current = v.RawBody
			default:
				return nil
			}
		default:
			return nil
		}
	}

	return current
}

// extractJSONPath extracts a value from JSON data using a simple path
func (s *Service) extractJSONPath(data interface{}, path string) (interface{}, error) {
	return s.getNestedValue(data, path), nil
}

// applyTemplate applies a Go template to data
func (s *Service) applyTemplate(templateStr string, data interface{}) (string, error) {
	tmpl, err := template.New("transform").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// applyRegex applies a regex transformation to string data
func (s *Service) applyRegex(data interface{}, pattern, replacement string) (string, error) {
	str := fmt.Sprintf("%v", data)
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("failed to compile regex: %w", err)
	}

	return regex.ReplaceAllString(str, replacement), nil
}

// getStringParam safely gets a string parameter from a map
func getStringParam(params map[string]interface{}, key string) string {
	if val, ok := params[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// getBoolParam safely gets a boolean parameter from a map with default
func getBoolParam(params map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := params[key]; ok {
		if boolVal, ok := val.(bool); ok {
			return boolVal
		}
	}
	return defaultVal
}

// PrintContext prints the current context (useful for debugging)
func (s *Service) PrintContext() {
	fmt.Println("=== Context ===")
	fmt.Println("Variables:")
	for k, v := range s.context.Variables {
		fmt.Printf("  %s: %v\n", k, v)
	}
	fmt.Println("Stage Outputs:")
	for k, v := range s.context.Outputs {
		fmt.Printf("  %s: %v\n", k, v)
	}
	fmt.Println("===============")
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run main.go <config.toml>")
		os.Exit(1)
	}

	configPath := os.Args[1]

	service, err := NewService(configPath)
	if err != nil {
		fmt.Printf("Failed to create service: %v\n", err)
		os.Exit(1)
	}

	if err := service.StartServer(); err != nil {
		fmt.Printf("Server failed: %v\n", err)
		os.Exit(1)
	}

}
