package e2etest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Azure/azure-storage-azcopy/v10/common"
	"io"
	"net/http"
	"net/url"
	"reflect"
)

type ARMSubject interface {
	Token() AccessToken
	ManagementURI() url.URL // todo: URL?
}

func CombineQuery(a, b url.Values) url.Values {
	out := make(url.Values)
	for k, v := range a {
		out[k] = append(out[k], v...)
	}
	for k, v := range b {
		out[k] = append(out[k], v...)
	}
	return out
}

// Ensure all types match interfaces
func init() {
	_ = []ARMSubject{&ARMClient{}, &ARMSubscription{}, &ARMResourceGroup{}}
}

// ARMUnimplementedStruct is used to fill in the blanks in types when implementation doesn't seem like a good use of time.
// It can be explored with "Get", if you know precisely what you want.
type ARMUnimplementedStruct json.RawMessage

func (s ARMUnimplementedStruct) Get(Key []string, out interface{}) error {
	if reflect.TypeOf(out).Kind() != reflect.Pointer {
		return errors.New("")
	}

	object := s
	for len(Key) > 0 {
		dict := make(map[string]json.RawMessage)
		err := json.Unmarshal(object, &dict)
		if err != nil {

		}

		object = ARMUnimplementedStruct(dict[Key[0]])
	}

	return json.Unmarshal(object, out)
}

type ARMClient struct {
	OAuth      AccessToken
	HttpClient *http.Client
}

func (c *ARMClient) getHTTPClient() *http.Client {
	if c.HttpClient != nil {
		return c.HttpClient
	}

	return http.DefaultClient
}

func (c *ARMClient) Token() AccessToken {
	return c.OAuth
}

func (c *ARMClient) ManagementURI() url.URL {
	uri, err := url.Parse("https://management.azure.com/")
	common.PanicIfErr(err) // should never happen

	return *uri
}

type ARMRequestSettings struct { // All values will be added to the request
	Method        string
	PathExtension string
	Query         url.Values
	Headers       http.Header
	Body          interface{}
}

func (s *ARMRequestSettings) CreateRequest(baseURI url.URL) (*http.Request, error) {
	query := baseURI.RawQuery
	if len(query) > 0 {
		query += "&"
	}
	query += s.Query.Encode()
	baseURI.RawQuery = query

	var body io.ReadSeeker
	if s.Body != nil {
		buf, err := json.Marshal(s.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal body: %w", err)
		}

		body = bytes.NewReader(buf)
	}

	newReq, err := http.NewRequest(s.Method, baseURI.String(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	newReq.Header = s.Headers

	return newReq, nil
}

// PerformRequest will deserialize to target (which assumes the target is a pointer)
// If an LRO is required, an *ARMAsyncResponse will be returned. Otherwise, both armResp and err will be nil, and target will be written to.
func (c *ARMClient) PerformRequest(baseURI url.URL, reqSettings ARMRequestSettings, target interface{}) (armResp *ARMAsyncResponse, err error) {
	client := c.getHTTPClient()

	r, err := reqSettings.CreateRequest(baseURI)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}

	oAuthToken, err := c.OAuth.FreshToken()
	r.Header["Authorization"] = []string{"Bearer " + oAuthToken}
	r.Header["Content-Type"] = []string{"application/json; charset=utf-8"}
	r.Header["Accept"] = []string{"application/json; charset=utf-8"}

	resp, err := client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	switch resp.StatusCode {
	case 200, 201: // immediate response
		var buf []byte // Read the body
		buf, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body (resp code 200): %w", err)
		}

		err = json.Unmarshal(buf, target)
		if err != nil {
			return nil, fmt.Errorf("failed to parse response body: %w", err)
		}

		return nil, nil
	case 202: // LRO pattern; grab Azure-AsyncOperation and resolve it.
		newTarget := resp.Header.Get("Azure-Asyncoperation")
		return ResolveAzureAsyncOperation(c.OAuth, newTarget, target)
	default:
		rBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body (resp code %d): %w", resp.StatusCode, err)
		}

		return nil, fmt.Errorf("failed to get access (resp code %d): %s", resp.StatusCode, string(rBody))
	}
}
