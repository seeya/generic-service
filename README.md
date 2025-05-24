Multi-Stage Action Service
A flexible Go service that executes multiple configurable actions based on TOML configuration files. The service supports HTTP requests, data transformations, file downloads, and context sharing between stages.

Features
HTTP Requests: Perform GET and POST requests with customizable headers
Data Transformation: Transform responses using JSON extraction, templates, and regex
File Downloads: Download files with variable-based output paths
Context Sharing: Access input/output from previous stages in subsequent stages
Variable Interpolation: Use Go templates for dynamic values
Installation
bash

# Initialize the module and install dependencies

go mod init action-service
go mod tidy
Usage
bash
go run main.go config.toml
Configuration Format
The service uses TOML configuration files with the following structure:

toml
[[stages]]
name = "stage_name"
action = "action_type"
[stages.parameters]

# action-specific parameters

Supported Actions

1. GET Request
   toml
   [[stages]]
   name = "fetch_data"
   action = "get"
   [stages.parameters]
   url = "https://api.example.com/data"
   [stages.parameters.headers]
   "Authorization" = "Bearer {{.variables.token}}"
   "User-Agent" = "MyApp/1.0"
2. POST Request
   toml
   [[stages]]
   name = "send_data"
   action = "post"
   [stages.parameters]
   url = "https://api.example.com/submit"
   [stages.parameters.headers]
   "Content-Type" = "application/json"
   [stages.parameters.body]
   name = "John Doe"
   email = "john@example.com"
3. Transform Data
   toml

# JSON Path extraction

[[stages]]
name = "extract_field"
action = "transform"
[stages.parameters]
input = "stages.fetch_data.body"
type = "json_extract"
path = "user.name"

# Template transformation

[[stages]]
name = "format_output"
action = "transform"
[stages.parameters]
input = "stages.fetch_data.body"
type = "template"
template = "Hello {{.name}}, your ID is {{.id}}"

# Regex transformation

[[stages]]
name = "clean_data"
action = "transform"
[stages.parameters]
input = "stages.fetch_data.body.email"
type = "regex"
pattern = "@.\*"
replacement = "@newdomain.com" 4. Download File
toml
[[stages]]
name = "download_file"
action = "download"
[stages.parameters]
url = "https://example.com/file.pdf"
output = "downloads/{{.variables.filename}}.pdf" 6. HTML Scraping
toml

# Basic element extraction

[[stages]]
name = "extract_links"
action = "scrape"
[stages.parameters]
input = "stages.fetch_page.raw_body"
selector = "a"

# Filter elements (equivalent to JS Array.filter)

[[stages]]
name = "book_links"
action = "scrape"
[stages.parameters]
input = "stages.fetch_page.raw_body"
selector = "a"
operation = "filter"
filter_type = "attribute"
attribute = "href"
filter_value = "/book/"
contains = true

# Get first element (equivalent to [0])

[[stages]]
name = "first_book_link"
action = "scrape"
[stages.parameters]
input = "stages.book_links"
selector = "a"
operation = "first"

# Map elements to extract specific data (equivalent to JS Array.map)

[[stages]]
name = "all_hrefs"
action = "scrape"
[stages.parameters]
input = "stages.fetch_page.raw_body"
selector = "a"
operation = "map"
map_type = "attribute"
attribute = "href"

# Count elements

[[stages]]
name = "link_count"
action = "scrape"
[stages.parameters]
input = "stages.fetch_page.raw_body"
selector = "a"
operation = "count"

# Extract by index

[[stages]]
name = "third_link"
action = "scrape"
[stages.parameters]
input = "stages.fetch_page.raw_body"
selector = "a"
operation = "index"
index = 2
toml
[[stages]]
name = "set_config"
action = "set_variable"
[stages.parameters]
name = "api_key"
value = "your-api-key-here"
Context and Variable Access
The service maintains a context that allows stages to access data from previous stages and variables:

Accessing Variables
toml
url = "{{.variables.api_base}}/endpoint"
Accessing Stage Outputs
toml

# Access the entire response

input = "stages.fetch_user"

# Access specific fields from HTTP responses

input = "stages.fetch_user.body.name"
input = "stages.fetch_user.status_code"
input = "stages.fetch_user.headers"

# Access transformed data

input = "stages.transform_step"
HTTP Response Structure
HTTP actions return responses with the following structure:

status_code: HTTP status code
headers: Response headers
body: Parsed JSON body (if JSON) or raw string
raw_body: Raw response body as string
Variable Interpolation
The service uses Go templates for variable interpolation. You can reference:

Variables: {{.variables.variable_name}}
Stage outputs: {{.stages.stage_name.field}}
Nested data: {{.stages.fetch_data.body.user.name}}
Example Workflow
The included config.toml demonstrates a complete workflow:

Set API base URL variable
Fetch user data via GET request
Extract user name from response
Set filename based on user data
Create a post via POST request
Generate summary from POST response
Download user avatar image
Clean email address with regex
Error Handling
The service will stop execution if any stage fails and provide detailed error messages indicating which stage failed and why.

Advanced Features
Nested JSON Access: Use dot notation to access nested JSON fields
Template Functions: Full Go template functionality for complex transformations
Header Interpolation: Dynamic headers using context variables
File Path Variables: Use context data in download file paths
Regex Patterns: Support for complex regex find-and-replace operations
Dependencies
github.com/BurntSushi/toml - TOML configuration parsing
github.com/PuerkitoBio/goquery - HTML parsing and CSS selector support
Running the Example
bash

# Run with the example configuration

go run main.go config.toml

# The service will:

# 1. Fetch user data from JSONPlaceholder API

# 2. Transform and use the data across multiple stages

# 3. Download a placeholder image

# 4. Show the complete context at the end
