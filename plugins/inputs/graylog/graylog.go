package graylog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/inputs"
)

type ResponseMetrics struct {
	total   int
	Metrics []Metric `json:"metrics"`
}

type Metric struct {
	FullName string                 `json:"full_name"`
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`
	Fields   map[string]interface{} `json:"metric"`
}

type GrayLog struct {
	Servers  []string
	Metrics  []string
	Username string
	Password string

	// Path to CA file
	SSLCA string `toml:"ssl_ca"`
	// Path to host cert file
	SSLCert string `toml:"ssl_cert"`
	// Path to cert key file
	SSLKey string `toml:"ssl_key"`
	// Use SSL but skip chain & host verification
	InsecureSkipVerify bool

	client HTTPClient
}

type HTTPClient interface {
	// Returns the result of an http request
	//
	// Parameters:
	// req: HTTP request object
	//
	// Returns:
	// http.Response:  HTTP respons object
	// error        :  Any error that may have occurred
	MakeRequest(req *http.Request) (*http.Response, error)

	SetHTTPClient(client *http.Client)
	HTTPClient() *http.Client
}

type Messagebody struct {
	Metrics []string `json:"metrics"`
}

type RealHTTPClient struct {
	client *http.Client
}

func (c *RealHTTPClient) MakeRequest(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

func (c *RealHTTPClient) SetHTTPClient(client *http.Client) {
	c.client = client
}

func (c *RealHTTPClient) HTTPClient() *http.Client {
	return c.client
}

var sampleConfig = `
  ## API End Point, currently supported API:
  ## - multiple  (Ex http://[graylog-server-ip]:12900/system/metrics/multiple)
  ## - namespace (Ex http://[graylog-server-ip]:12900/system/metrics/namespace/{namespace})
  ## Note if namespace end point specified metrics array will be ignored for that call.
  ## End point can contain namespace and multiple type calls
  ## Please check http://[graylog-server-ip]:12900/api-browser for full list end points
  servers = [
    "http://[graylog-server-ip]:12900/system/metrics/multiple",
  ]
  
  ## metrics list
  ## List of metrics can be found on Graylog webservice documentation 
  ## Or by hitting the the web service api http://[graylog-host]:12900/system/metrics  
  metrics = [
    "jvm.cl.loaded",
    "jvm.memory.pools.Metaspace.committed"
  ]

  ## User name and password  
  username = "put-username-here"
  password = "put-password-here"

  ## Optional SSL Config
  # ssl_ca = "/etc/telegraf/ca.pem"
  # ssl_cert = "/etc/telegraf/cert.pem"
  # ssl_key = "/etc/telegraf/key.pem"
  ## Use SSL but skip chain & host verification
  # insecure_skip_verify = false
`

func (h *GrayLog) SampleConfig() string {
	return sampleConfig
}

func (h *GrayLog) Description() string {
	return "Read flattened metrics from one or more GrayLog HTTP endpoints"
}

// Gathers data for all servers.
func (h *GrayLog) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	if h.client.HTTPClient() == nil {
		tlsCfg, err := internal.GetTLSConfig(
			h.SSLCert, h.SSLKey, h.SSLCA, h.InsecureSkipVerify)
		if err != nil {
			return err
		}
		tr := &http.Transport{
			ResponseHeaderTimeout: time.Duration(3 * time.Second),
			TLSClientConfig:       tlsCfg,
		}
		client := &http.Client{
			Transport: tr,
			Timeout:   time.Duration(4 * time.Second),
		}
		h.client.SetHTTPClient(client)
	}

	errorChannel := make(chan error, len(h.Servers))

	for _, server := range h.Servers {
		wg.Add(1)
		go func(server string) {
			defer wg.Done()
			if err := h.gatherServer(acc, server); err != nil {
				errorChannel <- err
			}
		}(server)
	}

	wg.Wait()
	close(errorChannel)

	// Get all errors and return them as one giant error
	errorStrings := []string{}
	for err := range errorChannel {
		errorStrings = append(errorStrings, err.Error())
	}

	if len(errorStrings) == 0 {
		return nil
	}
	return errors.New(strings.Join(errorStrings, "\n"))
}

// Gathers data from a particular server
// Parameters:
//     acc      : The telegraf Accumulator to use
//     serverURL: endpoint to send request to
//     service  : the service being queried
//
// Returns:
//     error: Any error that may have occurred
func (h *GrayLog) gatherServer(
	acc telegraf.Accumulator,
	serverURL string,
) error {
	resp, _, err := h.sendRequest(serverURL)
	if err != nil {
		return err
	}
	requestURL, err := url.Parse(serverURL)
	host, port, _ := net.SplitHostPort(requestURL.Host)
	var dat ResponseMetrics
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(resp), &dat); err != nil {
		return err
	}
	for _, m_item := range dat.Metrics {
		fields := make(map[string]interface{})
		tags := map[string]string{
			"server": host,
			"port":   port,
			"name":   m_item.Name,
			"type":   m_item.Type,
		}
		h.flatten(m_item.Fields, fields, "")
		acc.AddFields(m_item.FullName, fields, tags)
	}
	return nil
}

// Flatten JSON hierarchy to produce field name and field value
// Parameters:
//    item: Item map to flatten
//    fields: Map to store generated fields.
//    id: Prefix for top level metric (empty string "")
// Returns:
//    void
func (h *GrayLog) flatten(item map[string]interface{}, fields map[string]interface{}, id string) {
	if id != "" {
		id = id + "_"
	}
	for k, i := range item {
		switch i.(type) {
		case int:
			fields[id+k] = i.(float64)
		case float64:
			fields[id+k] = i.(float64)
		case map[string]interface{}:
			h.flatten(i.(map[string]interface{}), fields, id+k)
		default:
		}
	}
}

// Sends an HTTP request to the server using the GrayLog object's HTTPClient.
// Parameters:
//     serverURL: endpoint to send request to
//
// Returns:
//     string: body of the response
//     error : Any error that may have occurred
func (h *GrayLog) sendRequest(serverURL string) (string, float64, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}
	method := "GET"
	content := bytes.NewBufferString("")
	headers["Authorization"] = "Basic " + base64.URLEncoding.EncodeToString([]byte(h.Username+":"+h.Password))
	// Prepare URL
	requestURL, err := url.Parse(serverURL)
	if err != nil {
		return "", -1, fmt.Errorf("Invalid server URL \"%s\"", serverURL)
	}
	if strings.Contains(requestURL.String(), "multiple") {
		m := &Messagebody{Metrics: h.Metrics}
		http_body, err := json.Marshal(m)
		if err != nil {
			return "", -1, fmt.Errorf("Invalid list of Metrics %s", h.Metrics)
		}
		method = "POST"
		content = bytes.NewBuffer(http_body)
	}
	req, err := http.NewRequest(method, requestURL.String(), content)
	if err != nil {
		return "", -1, err
	}
	// Add header parameters
	for k, v := range headers {
		req.Header.Add(k, v)
	}
	start := time.Now()
	resp, err := h.client.MakeRequest(req)
	if err != nil {
		return "", -1, err
	}

	defer resp.Body.Close()
	responseTime := time.Since(start).Seconds()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return string(body), responseTime, err
	}

	// Process response
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("Response from url \"%s\" has status code %d (%s), expected %d (%s)",
			requestURL.String(),
			resp.StatusCode,
			http.StatusText(resp.StatusCode),
			http.StatusOK,
			http.StatusText(http.StatusOK))
		return string(body), responseTime, err
	}
	return string(body), responseTime, err
}

func init() {
	inputs.Add("graylog", func() telegraf.Input {
		return &GrayLog{
			client: &RealHTTPClient{},
		}
	})
}