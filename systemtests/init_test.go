package systemtests

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contiv/auth_proxy/auth"
	"github.com/contiv/auth_proxy/common"
	"github.com/contiv/auth_proxy/common/types"
	"github.com/contiv/auth_proxy/proxy"
	"github.com/contiv/auth_proxy/state"

	. "gopkg.in/check.v1"

	log "github.com/Sirupsen/logrus"
)

var (
	adminUsername = types.Admin.String()
	adminPassword = types.Admin.String()

	opsUsername = types.Ops.String()
	opsPassword = types.Ops.String()

	proxyHost = ""
)

// Test is the entrypoint for the systemtests suite.
// depending on the value of the DATASTORE_ADDRESS envvar, the tests will either run
// against etcd or consul.  the datastore is assumed to be fresh and with no
// existing state.
func Test(t *testing.T) {
	if len(os.Getenv("DEBUG")) > 0 {
		log.SetLevel(log.DebugLevel)
	}

	// PROXY_ADDRESS is set in ./scripts/systemtests_in_container.sh
	proxyHost = strings.TrimSpace(os.Getenv("PROXY_ADDRESS"))
	if 0 == len(proxyHost) {
		panic("you must supply a PROXY_ADDRESS (e.g., 1.2.3.4:12345)")
	}

	// DATASTORE_ADDRESS is set in ./scripts/systemtests_in_container.sh
	datastoreAddress := strings.TrimSpace(os.Getenv("DATASTORE_ADDRESS"))

	log.Info("Initializing datastore")
	if err := state.InitializeStateDriver(datastoreAddress); err != nil {
		log.Fatalln(err)
	}

	log.Info("Adding default users")
	if err := auth.AddDefaultUsers(); err != nil {
		log.Fatalln(err)
	}

	// set `tls_key_file` in Globals
	common.Global().Set("tls_key_file", "../local_certs/local.key")

	// execute the systemtests
	TestingT(t)
}

type systemtestSuite struct{}

var _ = Suite(&systemtestSuite{})

// ===== MISC FUNCTIONS =========================================================

// runTest is a convenience function which calls the passed in function and
// gives it a programmable MockServer as an argument.
// the auth_proxy:devbuild container which is started before any tests run is
// configured to use the MockServer as its "netmaster".
// see basic_test.go for some examples of how to use it.
func runTest(f func(*MockServer)) {
	ms := NewMockServer()

	// there is, however, no race condition on shutdown.  this blocks until the
	// listener is destroyed.
	defer ms.Stop()

	// TODO: there is a race condition here regarding the goroutine where
	//       http.Serve() is eventually called on the http.Server object
	//       created by NewMockServer(). Serve() does not provide any
	//       notification mechanism for when its listener is ready and it
	//       blocks when called, so there will be a very short window
	//       between us starting the proxy and mock servers and them actually
	//       being available to handle requests.
	//
	//       This ONLY affects the testing case and does not matter for the
	//       proxy server in general.
	//
	//       We could send HTTP requests in a loop until one succeeds on
	//       each server or something, but this is an acceptable stopgap
	//       for now.
	time.Sleep(100 * time.Millisecond)

	f(ms)
}

// login returns the user's token or returns an error if authentication fails.
func login(username, password string) (string, *http.Response, error) {
	type loginBody struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	lb := loginBody{
		Username: username,
		Password: password,
	}

	loginBytes, err := json.Marshal(&lb)
	if err != nil {
		return "", nil, err
	}

	resp, data, err := insecureJSONBody("", proxy.LoginPath, "POST", loginBytes)
	if err != nil {
		return "", resp, err
	}

	lr := proxy.LoginResponse{}
	err = json.Unmarshal(data, &lr)
	if err != nil {
		return "", resp, err
	}

	return lr.Token, resp, nil
}

// loginAs has the same functionality as login() but asserts rather than
// returning any errors.  You can use this to login when *not* testing login
// functionality if the adminToken() and opsToken() functions aren't more useful.
func loginAs(c *C, username, password string) string {
	token, resp, err := login(username, password)
	c.Assert(err, IsNil)
	c.Assert(resp.StatusCode, Equals, 200)
	c.Assert(len(token), Not(Equals), 0)

	return token
}

// adminToken logs in as the default admin user and returns a token or asserts.
func adminToken(c *C) string {
	return loginAs(c, adminUsername, adminPassword)
}

// opsToken logs in as the default ops user and returns a token or asserts.
func opsToken(c *C) string {
	return loginAs(c, opsUsername, opsPassword)
}

var insecureTestClient *http.Client

func init() {
	insecureTestClient = &http.Client{
		Transport: &http.Transport{
			// skip verification because MockServer uses a self-signed cert
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// proxyGet is a convenience function which sends an insecure HTTPS GET
// request to the proxy.
func proxyGet(c *C, token, path string) (*http.Response, []byte) {
	url := "https://" + proxyHost + path

	log.Debug("GET to ", url)

	req, err := http.NewRequest("GET", url, nil)
	c.Assert(err, IsNil)

	if len(token) > 0 {
		log.Debug("Setting X-Auth-token to:", token)
		req.Header.Set("X-Auth-Token", token)
	}

	resp, err := insecureTestClient.Do(req)
	c.Assert(err, IsNil)

	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	c.Assert(err, IsNil)

	return resp, data
}

// proxyDelete is a convenience function which sends an insecure HTTPS DELETE
// request to the proxy.
func proxyDelete(c *C, token, path string) (*http.Response, []byte) {
	url := "https://" + proxyHost + path

	log.Debug("GET to ", url)

	req, err := http.NewRequest("DELETE", url, nil)
	c.Assert(err, IsNil)

	if len(token) > 0 {
		log.Debug("Setting X-Auth-token to:", token)
		req.Header.Set("X-Auth-Token", token)
	}

	resp, err := insecureTestClient.Do(req)
	c.Assert(err, IsNil)

	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	c.Assert(err, IsNil)

	return resp, data
}

// proxyPatch is a convenience function which sends an insecure HTTPS PATCH
// request with the specified body to the proxy.
func proxyPatch(c *C, token, path string, body []byte) (*http.Response, []byte) {
	resp, body, err := insecureJSONBody(token, path, "PATCH", body)
	c.Assert(err, IsNil)

	return resp, body
}

// proxyPost is a convenience function which sends an insecure HTTPS POST
// request with the specified body to the proxy.
func proxyPost(c *C, token, path string, body []byte) (*http.Response, []byte) {
	resp, body, err := insecureJSONBody(token, path, "POST", body)
	c.Assert(err, IsNil)

	return resp, body
}

// proxyPut is a convenience function which sends an insecure HTTPS PUT
// request with the specified body to the proxy.
func proxyPut(c *C, token, path string, body []byte) (*http.Response, []byte) {
	resp, body, err := insecureJSONBody(token, path, "PUT", body)
	c.Assert(err, IsNil)

	return resp, body
}

// insecureJSONBody sends an insecure HTTPS POST request with the specified
// JSON payload as the body.
func insecureJSONBody(token, path, requestType string, body []byte) (*http.Response, []byte, error) {
	url := "https://" + proxyHost + path

	log.Debug(requestType, " to ", url)

	req, err := http.NewRequest(requestType, url, bytes.NewBuffer(body))
	if err != nil {
		log.Debugf("%v request creation failed: %s", requestType, err)
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if len(token) > 0 {
		log.Debug("Setting X-Auth-token to:", token)
		req.Header.Set("X-Auth-Token", token)
	}

	resp, err := insecureTestClient.Do(req)
	if err != nil {
		log.Debugf("%v request failed: %s", requestType, err)
		return nil, nil, err
	}

	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Debugf("Failed to read response body: %s", err)
		return nil, nil, err
	}

	return resp, data, nil
}
